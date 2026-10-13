package imap

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

// poolTestServer counts connections and can cut every open one, as a server
// restart or an idle timeout would.
type poolTestServer struct {
	cfg         *models.AccountConfig
	connections atomic.Int32
	mu          sync.Mutex
	open        []net.Conn
}

func (s *poolTestServer) cutAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.open {
		_ = conn.Close()
	}
	s.open = nil
}

func startPoolTestServer(t *testing.T) *poolTestServer {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("user@example.com", "secret")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         goimap.CapSet{goimap.CapIMAP4rev1: {}, goimap.CapIMAP4rev2: {}},
	})
	serverListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(serverListener) }()
	t.Cleanup(func() { _ = server.Close() })

	s := &poolTestServer{}
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	go func() {
		for {
			down, err := proxy.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", serverListener.Addr().String())
			if err != nil {
				_ = down.Close()
				return
			}
			s.connections.Add(1)
			s.mu.Lock()
			s.open = append(s.open, down, up)
			s.mu.Unlock()
			go func() { _, _ = io.Copy(down, up); _ = down.Close() }()
			go func() { _, _ = io.Copy(up, down); _ = up.Close() }()
		}
	}()
	host, portString, _ := net.SplitHostPort(proxy.Addr().String())
	port, _ := strconv.Atoi(portString)
	s.cfg = &models.AccountConfig{
		AccountID: "acc", IMAPHost: host, IMAPPort: port, IMAPAllowPrivate: true,
		IMAPTLSMode: "plaintext", IMAPAllowPlaintext: true, AuthMethod: "plain", Username: "user@example.com",
	}
	return s
}

func newTestPool(t *testing.T, options PoolOptions) *Pool {
	t.Helper()
	pool := NewPool(options)
	t.Cleanup(pool.Close)
	return pool
}

func TestPoolReusesHealthySessions(t *testing.T) {
	server := startPoolTestServer(t)
	pool := newTestPool(t, PoolOptions{})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		client, err := pool.Get(ctx, server.cfg, "secret")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := client.FetchAllUIDs(ctx, "INBOX", 0); err != nil {
			t.Fatal(err)
		}
		client.Close()
	}
	if got := server.connections.Load(); got != 1 {
		t.Fatalf("three uses opened %d connections, want 1", got)
	}
	if got := pool.IdleSessions(); got != 1 {
		t.Fatalf("idle sessions = %d, want 1", got)
	}
}

func TestPoolSeparatesCredentials(t *testing.T) {
	server := startPoolTestServer(t)
	pool := newTestPool(t, PoolOptions{})
	ctx := context.Background()
	client, err := pool.Get(ctx, server.cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	// A changed password must not reuse the session opened with the old one.
	if _, err := pool.Get(ctx, server.cfg, "changed"); err == nil {
		t.Fatal("session reused for different credentials")
	}
	other := *server.cfg
	other.AccountID = "another"
	client, err = pool.Get(ctx, &other, "secret")
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	if got := server.connections.Load(); got != 3 {
		t.Fatalf("connections = %d, want one per account and credential", got)
	}
}

func TestPoolReplacesSessionsTheServerDropped(t *testing.T) {
	server := startPoolTestServer(t)
	pool := newTestPool(t, PoolOptions{CheckAfter: time.Nanosecond})
	ctx := context.Background()
	client, err := pool.Get(ctx, server.cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	server.cutAll()
	client, err = pool.Get(ctx, server.cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.ListFolders(ctx); err != nil {
		t.Fatalf("reused a dead session: %v", err)
	}
	if got := server.connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want a new one after the drop", got)
	}
}

func TestPoolDiscardsCancelledAndSelectedSessions(t *testing.T) {
	server := startPoolTestServer(t)
	pool := newTestPool(t, PoolOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	client, err := pool.Get(ctx, server.cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond) // Let the cancellation close it.
	client.Close()
	if got := pool.IdleSessions(); got != 0 {
		t.Fatalf("cancelled session kept: %d idle", got)
	}

	client, err = pool.Get(context.Background(), server.cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.client.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	client.Close()
	if got := pool.IdleSessions(); got != 0 {
		t.Fatalf("session with a selected folder kept: %d idle", got)
	}
}

func TestPoolDropsAccountsAndExpiresIdleSessions(t *testing.T) {
	server := startPoolTestServer(t)
	pool := newTestPool(t, PoolOptions{IdleTimeout: 50 * time.Millisecond})
	ctx := context.Background()
	client, err := pool.Get(ctx, server.cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	pool.DropAccount("acc")
	if got := pool.IdleSessions(); got != 0 {
		t.Fatalf("dropped account still has %d idle sessions", got)
	}

	client, err = pool.Get(ctx, server.cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	deadline := time.Now().Add(2 * time.Second)
	for pool.IdleSessions() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := pool.IdleSessions(); got != 0 {
		t.Fatalf("idle session not expired: %d", got)
	}
}

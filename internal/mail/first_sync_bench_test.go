package mail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/cristianadrielbraun/gofer/internal/config"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

type benchLiteral struct{ *bytes.Reader }

func (l benchLiteral) Size() int64 { return int64(l.Len()) }

// firstSyncBenchMessage is an invented message in a conversation of three,
// with one or two recipients, a copy now and then and a short body.
func firstSyncBenchMessage(n int) []byte {
	thread := n / 3
	var refs string
	if n%3 != 0 {
		refs = fmt.Sprintf("In-Reply-To: <bench-%d@example.test>\r\nReferences: <bench-%d@example.test> <bench-%d@example.test>\r\n", n-1, thread*3, n-1)
	}
	cc := ""
	if n%4 == 0 {
		cc = fmt.Sprintf("Cc: Copy Person %d <copy%d@example.test>\r\n", n%40, n%40)
	}
	subject := fmt.Sprintf("Planning notes %d", thread)
	if n%3 != 0 {
		subject = "Re: " + subject
	}
	date := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(n) * 17 * time.Minute)
	return []byte(fmt.Sprintf("From: Sender %d <sender%d@example.test>\r\n"+
		"To: Reader <reader@example.test>, Team %d <team%d@example.test>\r\n%s"+
		"Subject: %s\r\nDate: %s\r\nMessage-ID: <bench-%d@example.test>\r\n%s"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
		"Hello, these are the notes for item %d.\r\nThanks.\r\n",
		n%300, n%300, n%20, n%20, cc, subject, date.Format(time.RFC1123Z), n, refs, n))
}

// BenchmarkIMAPFirstSync times downloading a mailbox for the first time:
// listing envelopes over IMAP and saving them. GOFER_BENCH_FIRST_SYNC sets
// the number of messages (default 20,000).
func BenchmarkIMAPFirstSync(b *testing.B) {
	total := 20000
	if value := os.Getenv("GOFER_BENCH_FIRST_SYNC"); value != "" {
		total, _ = strconv.Atoi(value)
	}
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(os.Stderr) })
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("reader@example.test", "secret")
	if err := user.Create("INBOX", nil); err != nil {
		b.Fatal(err)
	}
	if err := user.Create("Archive", nil); err != nil {
		b.Fatal(err)
	}
	for n := 1; n <= total; n++ {
		folder := "INBOX"
		if n%5 == 0 {
			folder = "Archive"
		}
		var flags []goimap.Flag
		if n%7 != 0 {
			flags = append(flags, goimap.FlagSeen)
		}
		if n%50 == 0 {
			flags = append(flags, goimap.FlagFlagged)
		}
		raw := firstSyncBenchMessage(n)
		if _, err := user.Append(folder, benchLiteral{bytes.NewReader(raw)}, &goimap.AppendOptions{Flags: flags}); err != nil {
			b.Fatal(err)
		}
	}
	mem.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Caps:         goimap.CapSet{goimap.CapIMAP4rev1: {}},
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	b.Cleanup(func() { _ = server.Close() })
	host, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)

	ctx := context.Background()
	for b.Loop() {
		b.StopTimer()
		db, err := storage.New(filepath.Join(b.TempDir(), "gofer.db"))
		if err != nil {
			b.Fatal(err)
		}
		if _, err := db.Write().Exec(`INSERT INTO users (id, username, username_normalized, name, is_admin, user_type) VALUES ('default', 'default', 'default', 'Local', 0, 'webmail')`); err != nil {
			b.Fatal(err)
		}
		if err := db.AddPlaintextTransportException(ctx, "imap", host, port, "default"); err != nil {
			b.Fatal(err)
		}
		if err := db.AddPrivateTargetException(ctx, "imap", host, port, "default"); err != nil {
			b.Fatal(err)
		}
		accounts, err := config.NewAccountStore(db, []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			b.Fatal(err)
		}
		account, err := accounts.CreateAccount(ctx, "default", &models.CreateAccountRequest{
			EmailAddress: "reader@example.test", IMAPHost: host, IMAPPort: port, IMAPTLSMode: "plaintext",
			SMTPHost: "smtp.example.test", Username: "reader@example.test", Password: "secret",
		})
		if err != nil {
			b.Fatal(err)
		}
		orchestrator := NewSyncOrchestrator(db, accounts, nil, nil)
		b.StartTimer()
		if err := orchestrator.syncAccount(ctx, account.ID, true); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		var saved int
		if err := db.Read().QueryRow(`SELECT count(*) FROM messages`).Scan(&saved); err != nil || saved != total {
			b.Fatalf("saved %d messages, want %d (%v)", saved, total, err)
		}
		_ = db.Close()
		b.StartTimer()
	}
	b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(b.N)/float64(total), "µs/message")
}

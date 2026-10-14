package imap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"testing"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

type testLiteral struct{ *bytes.Reader }

func (l testLiteral) Size() int64 { return int64(l.Len()) }

// startBatchTestClient serves an inbox of count invented messages and
// returns a client logged in to it.
func startBatchTestClient(t *testing.T, count int) *Client {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser("user@example.com", "secret")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= count; n++ {
		raw := fmt.Sprintf("From: Sender <sender@example.com>\r\nTo: user@example.com\r\nMessage-ID: <batch-%d@example.com>\r\nSubject: Note %d\r\nDate: Thu, 01 Oct 2026 09:00:00 +0000\r\n\r\nBody %d\r\n", n, n, n)
		if _, err := user.Append("INBOX", testLiteral{bytes.NewReader([]byte(raw))}, &goimap.AppendOptions{}); err != nil {
			t.Fatal(err)
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
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	host, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	client, err := NewClient(context.Background(), &models.AccountConfig{
		AccountID: "acc", IMAPHost: host, IMAPPort: port, IMAPAllowPrivate: true,
		IMAPTLSMode: "plaintext", IMAPAllowPlaintext: true, AuthMethod: "plain", Username: "user@example.com",
	}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// inboxUIDValidity asks the server for the inbox's UIDVALIDITY, which an
// incremental sync must match.
func inboxUIDValidity(t *testing.T, client *Client) uint32 {
	t.Helper()
	result, err := client.SyncFolderIncremental(context.Background(), "inbox", "INBOX", 1, 0, func([]storage.SyncMessage) error { return nil })
	if err != nil || !result.UIDValidityChanged {
		t.Fatalf("UIDVALIDITY probe: %+v, %v", result, err)
	}
	return result.UIDValidity
}

func TestSyncFolderIncrementalSavesLongGapsInBatches(t *testing.T) {
	client := startBatchTestClient(t, 1203)
	validity := inboxUIDValidity(t, client)
	var sizes []int
	next := uint32(2)
	result, err := client.SyncFolderIncremental(context.Background(), "inbox", "INBOX", 1, validity, func(messages []storage.SyncMessage) error {
		sizes = append(sizes, len(messages))
		for _, message := range messages {
			if message.RemoteUID != next {
				return fmt.Errorf("UID %d, want %d", message.RemoteUID, next)
			}
			next++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizes) != "[500 500 202]" {
		t.Fatalf("batches = %v, want [500 500 202]", sizes)
	}
	if result.HighestUID != 1203 || result.TotalFetched != 1202 {
		t.Fatalf("result = highest %d, fetched %d; want 1203, 1202", result.HighestUID, result.TotalFetched)
	}
}

// A batch that fails to save stops the sync with its error, and the session
// stays usable: the rest of the server's answer is read, not left behind.
func TestFolderSyncStopsAtFailedBatchAndKeepsSession(t *testing.T) {
	client := startBatchTestClient(t, 1203)
	validity := inboxUIDValidity(t, client)
	failure := errors.New("disk full")
	calls := 0
	_, err := client.SyncFolderIncremental(context.Background(), "inbox", "INBOX", 1, validity, func([]storage.SyncMessage) error {
		calls++
		return failure
	})
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("incremental: err = %v after %d batches, want %v after 1", err, calls, failure)
	}

	calls = 0
	_, err = client.SyncFolder(context.Background(), "inbox", "INBOX", FolderSyncOptions{ChunkSize: 100}, func([]storage.SyncMessage) error {
		calls++
		if calls == 2 {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || calls != 2 {
		t.Fatalf("full: err = %v after %d batches, want %v after 2", err, calls, failure)
	}

	saved := 0
	if _, err := client.SyncFolder(context.Background(), "inbox", "INBOX", FolderSyncOptions{ChunkSize: 500}, func(messages []storage.SyncMessage) error {
		saved += len(messages)
		return nil
	}); err != nil || saved != 1203 {
		t.Fatalf("after failures: saved %d, err %v; want 1203, nil", saved, err)
	}
}

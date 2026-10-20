package mail

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/config"
	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// While JMAP is turned off, a JMAP account is paused: syncing it reaches no
// server and records no error, its queued changes wait without bringing the
// next sync forward, and an uncached message isn't downloaded. Turned on
// again, the queued changes go out.
func TestJMAPAccountPausesWhileJMAPIsOff(t *testing.T) {
	f := newUserGmailFixture(t)
	server := newFakeJMAPServer(t)
	server.setMailboxes(jmap.Mailbox{ID: "mb-in", Name: "Inbox", Role: "inbox", MyRights: readable()})
	server.emails = []jmap.Email{testJMAPEmail("e1", "Lunch on Friday?", 1, "mb-in"), testJMAPEmail("e2", "Garden plans", 2, "mb-in")}
	var requests atomic.Int64
	handler := server.Config.Handler
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	})
	useFakeJMAPAccount(t, f, server)
	if err := f.worker.Sync(t.Context(), "alice", f.ids["alice"]); err != nil {
		t.Fatal(err)
	}
	local := func(providerID string) int64 {
		var id int64
		f.local(t, "alice", func(db *storage.DB) error {
			return db.Read().QueryRow(`SELECT id FROM messages WHERE account_id=? AND remote_message_id=?`, f.ids["alice"], providerID).Scan(&id)
		})
		return id
	}
	f.local(t, "alice", func(db *storage.DB) error { return db.SetMessageReadAndQueue(t.Context(), local("e1"), true) })

	t.Setenv("GOFER_JMAP_EXPERIMENT", "false")
	before, sets := requests.Load(), server.sets
	if err := f.worker.Sync(t.Context(), "alice", f.ids["alice"]); err != nil {
		t.Fatalf("Sync() while JMAP is off = %v", err)
	}
	if requests.Load() != before || server.sets != sets {
		t.Fatalf("reached the server %d times while JMAP is off", requests.Load()-before)
	}
	var syncError string
	f.local(t, "alice", func(db *storage.DB) error {
		return db.Read().QueryRow(`SELECT COALESCE(email_sync_error,'') FROM accounts WHERE id=?`, f.ids["alice"]).Scan(&syncError)
	})
	if syncError != "" {
		t.Fatalf("pausing recorded a sync error: %q", syncError)
	}
	var due int64
	if err := f.system.Read().QueryRow(`SELECT next_due_ms FROM gofer_account_poll_schedule WHERE account_id=?`, f.ids["alice"]).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if time.UnixMilli(due).Before(time.Now().Add(time.Minute)) {
		t.Fatalf("the queued change brought the next sync forward to %s", time.UnixMilli(due))
	}
	if err := f.worker.EnsureBody(t.Context(), "alice", local("e2")); !errors.Is(err, providers.ErrJMAPDisabled) {
		t.Fatalf("EnsureBody() while JMAP is off = %v", err)
	}
	if requests.Load() != before {
		t.Fatal("opening a message reached the server while JMAP is off")
	}

	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	if err := f.worker.Sync(t.Context(), "alice", f.ids["alice"]); err != nil {
		t.Fatal(err)
	}
	if server.sets == sets {
		t.Fatal("the change queued while JMAP was off was not sent once it was on")
	}
}

// In single-user mode too, syncing a JMAP account while JMAP is off reaches
// no server and reports no error.
func TestSharedJMAPSyncPausesWhileJMAPIsOff(t *testing.T) {
	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	ctx := t.Context()
	server := newFakeJMAPServer(t)
	server.setMailboxes(jmap.Mailbox{ID: "mb-in", Name: "Inbox", Role: "inbox", MyRights: readable()})
	var requests atomic.Int64
	handler := server.Config.Handler
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	})
	db := newLabelSyncTestDB(t)
	endpoint, err := jmap.ParseEndpoint(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddPlaintextTransportException(ctx, "http", endpoint.Host, endpoint.Port, "default"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddPrivateTargetException(ctx, "http", endpoint.Host, endpoint.Port, "default"); err != nil {
		t.Fatal(err)
	}
	accounts, err := config.NewAccountStore(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.CreateAccount(ctx, "default", &models.CreateAccountRequest{
		Provider: "jmap", EmailAddress: "robin@example.test", JMAPSessionURL: server.URL + "/.well-known/jmap",
		Username: "robin@example.test", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	orchestrator := NewSyncOrchestrator(db, accounts, nil, nil)
	t.Setenv("GOFER_JMAP_EXPERIMENT", "false")
	if err := orchestrator.syncAccount(ctx, account.ID, true); err != nil || requests.Load() != 0 {
		t.Fatalf("syncAccount() while JMAP is off = %v after %d requests", err, requests.Load())
	}
	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	if err := orchestrator.syncAccount(ctx, account.ID, true); err != nil || requests.Load() == 0 {
		t.Fatalf("syncAccount() with JMAP on = %v after %d requests", err, requests.Load())
	}
}

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	stdmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/auth"
	"github.com/cristianadrielbraun/gofer/internal/config"
	"github.com/cristianadrielbraun/gofer/internal/mail/message"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// While JMAP is turned off, refreshing all accounts refreshes the others and
// leaves JMAP accounts out, or says why when they are all there is. A JMAP
// account alone isn't found, like its other controls.
func TestUserManualSyncSkipsJMAPAccountsWhileJMAPIsOff(t *testing.T) {
	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	f := newUserContactPushFixture(t, "carddav", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	jmapAccount, err := f.h.userAccounts.CreateAccount(t.Context(), "alice", &models.CreateAccountRequest{
		Provider: "jmap", EmailAddress: "alice@jmap.example.test", JMAPSessionURL: "https://jmap.example.test/.well-known/jmap",
		Username: "alice@jmap.example.test", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	imapAccount := f.accounts["alice"]
	for _, id := range []string{imapAccount.ID, jmapAccount.ID} {
		if err := f.h.userAccounts.WithAccountForUser(t.Context(), "alice", id, func(_ *config.AccountStore, db *storage.DB) error {
			_, err := db.Write().Exec(`UPDATE accounts SET email_sync_enabled=1 WHERE id=?`, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GOFER_JMAP_EXPERIMENT", "false")
	refresh := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/sync", nil)
		req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: "alice"}))
		if id != "" {
			req.SetPathValue("id", id)
		}
		rec := httptest.NewRecorder()
		f.h.handleUserManualSync(rec, req)
		return rec
	}
	// Errors answer 200 with X-Gofer-Status: error, so the page shows them.
	if rec := refresh(""); rec.Header().Get("X-Gofer-Status") != "ok" || rec.Header().Get("X-Gofer-Mail-Sync-Accounts") != "1" {
		t.Fatalf("refresh all = %d %q, accounts %q; want the IMAP account only", rec.Code, rec.Body.String(), rec.Header().Get("X-Gofer-Mail-Sync-Accounts"))
	}
	if rec := refresh(jmapAccount.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("refresh the JMAP account = %d %q", rec.Code, rec.Body.String())
	}
	if err := f.h.userAccounts.WithAccountForUser(t.Context(), "alice", imapAccount.ID, func(_ *config.AccountStore, db *storage.DB) error {
		_, err := db.Write().Exec(`UPDATE accounts SET email_sync_enabled=0 WHERE id=?`, imapAccount.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rec := refresh(""); rec.Header().Get("X-Gofer-Status") != "error" || !strings.Contains(rec.Body.String(), providers.ErrJMAPDisabled.Error()) {
		t.Fatalf("refresh all with only a JMAP account = %q", rec.Body.String())
	}
}

// A message can't be sent or scheduled from a JMAP account while JMAP is
// turned off; the reason names the setting.
func TestQueueOutgoingMessageRefusesJMAPWhileJMAPIsOff(t *testing.T) {
	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	f := newUserContactPushFixture(t, "carddav", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	account, err := f.h.userAccounts.CreateAccount(t.Context(), "alice", &models.CreateAccountRequest{
		Provider: "jmap", EmailAddress: "alice@jmap.example.test", JMAPSessionURL: "https://jmap.example.test/.well-known/jmap",
		Username: "alice@jmap.example.test", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFER_JMAP_EXPERIMENT", "false")
	msg := &message.OutgoingMessage{FromEmail: "alice@jmap.example.test", MessageID: "<lunch-2@example.test>", Subject: "Lunch?",
		To: []*stdmail.Address{{Address: "sam@example.test"}}, TextBody: "Noon?"}
	if err := f.h.userAccounts.WithUser(t.Context(), "alice", func(local *config.AccountStore, db *storage.DB) error {
		h := &Handler{db: db, accountStore: local}
		_, err := h.queueOutgoingMessage(t.Context(), account.ID, 0, "", msg, time.Now(), false)
		if !errors.Is(err, providers.ErrJMAPDisabled) {
			t.Fatalf("queueOutgoingMessage() while JMAP is off = %v", err)
		}
		var queued int
		if err := db.Read().QueryRow(`SELECT COUNT(*) FROM outgoing_sends WHERE account_id=?`, account.ID).Scan(&queued); err != nil || queued != 0 {
			t.Fatalf("queued %d sends while JMAP is off, %v", queued, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

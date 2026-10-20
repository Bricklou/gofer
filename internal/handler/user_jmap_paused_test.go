package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/auth"
	"github.com/cristianadrielbraun/gofer/internal/config"
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

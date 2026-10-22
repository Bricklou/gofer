package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/auth"
	"github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func newOutlookPushHandler(t *testing.T) (*Handler, *http.ServeMux, context.Context) {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Write().Exec(`INSERT OR IGNORE INTO users (id, username, username_normalized, name) VALUES ('default', 'default', 'default', 'Default'), ('other', 'other', 'other', 'Other');
 INSERT INTO accounts (id, user_id, provider, email_address) VALUES ('out', 'default', 'outlook', 'robin@example.com'), ('imap', 'default', 'imap', 'robin@example.org'), ('theirs', 'other', 'outlook', 'sam@example.com')`); err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db, syncer: mail.NewSyncOrchestrator(db, nil, nil, nil)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/settings/accounts/{id}/outlook-push", h.handleOutlookPushSetting)
	mux.HandleFunc("POST /api/settings/accounts/{id}/outlook-push/check", h.handleOutlookPushCheck)
	return h, mux, auth.ContextWithUser(context.Background(), &auth.User{ID: "default"})
}

func postOutlookPush(t *testing.T, mux *http.ServeMux, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOutlookPushSettingSavesTheOwnersChoice(t *testing.T) {
	h, mux, ctx := newOutlookPushHandler(t)
	rec := postOutlookPush(t, mux, ctx, "/api/settings/accounts/out/outlook-push", "enabled=false")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="outlook-push-out"`) {
		t.Fatalf("choose scheduled checks = %d %s", rec.Code, rec.Body.String())
	}
	if push, err := h.db.ProviderPushEnabled(ctx, "out"); err != nil || push {
		t.Fatalf("saved choice = %t %v", push, err)
	}
	for _, path := range []string{"/api/settings/accounts/imap/outlook-push", "/api/settings/accounts/theirs/outlook-push", "/api/settings/accounts/missing/outlook-push"} {
		if rec := postOutlookPush(t, mux, ctx, path, "enabled=false"); rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want not found", path, rec.Code)
		}
	}
	if push, err := h.db.ProviderPushEnabled(ctx, "theirs"); err != nil || !push {
		t.Fatalf("another user's account changed: %t %v", push, err)
	}
	if rec := postOutlookPush(t, mux, ctx, "/api/settings/accounts/out/outlook-push", "enabled=yes"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid choice = %d", rec.Code)
	}
}

func TestOutlookPushCheckWithNotificationsOff(t *testing.T) {
	_, mux, ctx := newOutlookPushHandler(t)
	rec := postOutlookPush(t, mux, ctx, "/api/settings/accounts/out/outlook-push/check", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "GOFER_OUTLOOK_WEBHOOKS=true") {
		t.Fatalf("check with notifications off = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postOutlookPush(t, mux, ctx, "/api/settings/accounts/theirs/outlook-push/check", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("check of another user's account = %d", rec.Code)
	}
}

func TestBuildSyncSettingsDescribesOutlookPush(t *testing.T) {
	h, _, ctx := newOutlookPushHandler(t)
	settings := h.buildSyncSettings(ctx, []models.Account{
		{ID: "out", Provider: "outlook", EmailSyncEnabled: true},
		{ID: "imap", Provider: "imap", EmailSyncEnabled: true},
	})
	if len(settings.Accounts) != 2 || settings.Accounts[0].OutlookPush == nil || settings.Accounts[1].OutlookPush != nil {
		t.Fatalf("accounts = %+v", settings.Accounts)
	}
	if push := settings.Accounts[0].OutlookPush; !push.Enabled || push.Available || push.Managed {
		t.Fatalf("Outlook push with notifications off = %+v", push)
	}
}

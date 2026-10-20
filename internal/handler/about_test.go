package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/mail"
)

// Personal and managed mode serve signed-in users from the per-user routes,
// so the About page's update check has to be mounted there too.
func TestUserStorageRoutesServeAboutUpdateCheck(t *testing.T) {
	f := verifiedOwnedStatusFixture(t)
	root, cancel := context.WithCancel(t.Context())
	syncer := mail.NewSyncOrchestrator(f.system, nil, nil, nil)
	imap, err := mail.NewUserIMAP(root, f.h.userAccounts, f.h.blobStore, syncer.Events())
	if err != nil {
		t.Fatal(err)
	}
	h := New(f.system, nil, syncer, f.h.blobStore, f.manager, "")
	t.Cleanup(func() { cancel(); imap.Wait(); h.WaitAvatarWorkers() })
	mux := http.NewServeMux()
	if err := h.RegisterUserStorageRoutes(root, mux, f.h.userStorage, UserStorageOptions{Accounts: f.h.userAccounts, IMAP: imap}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/settings/about/update-check", nil)
	if _, pattern := mux.Handler(req); pattern != "POST /api/settings/about/update-check" {
		t.Fatalf("update check is not routed for users; matched %q", pattern)
	}
}

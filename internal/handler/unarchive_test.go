package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/auth"
	"github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func TestUnarchiveMovesOnlyArchivedThreadMessagesToInbox(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Write().ExecContext(ctx, `INSERT OR IGNORE INTO users (id, username, username_normalized, name) VALUES ('default', 'default', 'default', 'Default')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, provider, email_address) VALUES ('acc', 'default', ?, 'user@example.com')`, providers.ProviderIMAP); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.UpsertFolders(ctx, []storage.UpsertFolderInput{
		{ID: "acc_inbox", AccountID: "acc", RemoteID: "INBOX", Name: "Inbox", Role: "inbox", Selectable: true},
		{ID: "acc_archive", AccountID: "acc", RemoteID: "Archive", Name: "Archive", Role: "archive", Selectable: true},
		{ID: "acc_sent", AccountID: "acc", RemoteID: "Sent", Name: "Sent", Role: "sent", Selectable: true},
	}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	now := time.Now()
	if err := db.UpsertSyncMessages(ctx, []storage.SyncMessage{
		{AccountID: "acc", FolderID: "acc_archive", RemoteUID: 7, MessageID: "<picnic@example.com>", Subject: "Picnic plans", FromEmail: "friend@example.com", DateSent: now.Add(-time.Hour), IsRead: true},
		{AccountID: "acc", FolderID: "acc_sent", RemoteUID: 8, MessageID: "<picnic-reply@example.com>", InReplyTo: "<picnic@example.com>", References: "<picnic@example.com>", Subject: "Re: Picnic plans", FromEmail: "user@example.com", DateSent: now, IsRead: true},
	}); err != nil {
		t.Fatalf("UpsertSyncMessages() error = %v", err)
	}
	archivedID, err := db.GetMessageLocalIDByInternetIDInternal(ctx, "acc", "<picnic@example.com>")
	if err != nil || archivedID == 0 {
		t.Fatalf("GetMessageLocalIDByInternetIDInternal() = %d, %v", archivedID, err)
	}
	sentID, err := db.GetMessageLocalIDByInternetIDInternal(ctx, "acc", "<picnic-reply@example.com>")
	if err != nil || sentID == 0 {
		t.Fatalf("GetMessageLocalIDByInternetIDInternal() = %d, %v", sentID, err)
	}

	h := &Handler{db: db, syncer: mail.NewSyncOrchestrator(db, nil, nil, nil)}
	body := `{"targets":[{"id":"` + strconv.FormatInt(archivedID, 10) + `","thread":true}],"folder_id":"acc_archive"}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages/unarchive", strings.NewReader(body))
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: "default"}))
	rec := httptest.NewRecorder()

	h.handleUnarchiveMessages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	folderOf := func(messageID int64) string {
		t.Helper()
		var folderID string
		if err := db.Read().QueryRowContext(ctx, `SELECT folder_id FROM message_folder_state WHERE message_id = ? AND is_deleted = 0`, messageID).Scan(&folderID); err != nil {
			t.Fatalf("load folder of %d: %v", messageID, err)
		}
		return folderID
	}
	if got := folderOf(archivedID); got != "acc_inbox" {
		t.Fatalf("archived message folder = %q, want acc_inbox", got)
	}
	if got := folderOf(sentID); got != "acc_sent" {
		t.Fatalf("sent reply folder = %q, want acc_sent", got)
	}
}

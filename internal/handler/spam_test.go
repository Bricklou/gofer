package handler

import (
	"context"
	"encoding/json"
	"github.com/cristianadrielbraun/gofer/internal/auth"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func TestSpamActionFallsBackToLocalMoveWhenRemoteReportFails(t *testing.T) {
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
		{ID: "acc_inbox", AccountID: "acc", Name: "Inbox", Role: "inbox", Selectable: true},
		{ID: "acc_spam", AccountID: "acc", Name: "Spam", Role: "junk", Selectable: true},
	}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	if err := db.UpsertSyncMessages(ctx, []storage.SyncMessage{{
		AccountID: "acc",
		FolderID:  "acc_inbox",
		RemoteUID: 42,
		MessageID: "<spam-fallback@example.com>",
		Subject:   "Spam fallback",
		FromEmail: "sender@example.com",
		DateSent:  time.Now(),
		IsRead:    true,
	}}); err != nil {
		t.Fatalf("UpsertSyncMessages() error = %v", err)
	}
	msgID, err := db.GetMessageLocalIDByInternetIDInternal(ctx, "acc", "<spam-fallback@example.com>")
	if err != nil || msgID == 0 {
		t.Fatalf("GetMessageLocalIDByInternetIDInternal() = %d, %v", msgID, err)
	}

	h := &Handler{db: db, syncer: mail.NewSyncOrchestrator(db, nil, nil, nil)}
	body := `{"targets":[{"id":"` + strings.TrimSpace(strconv.FormatInt(msgID, 10)) + `"}],"folder_id":"acc_inbox"}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages/spam", strings.NewReader(body))
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: "default"}))
	rec := httptest.NewRecorder()

	h.handleMarkMessagesSpam(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var response map[string]int
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["messages"] != 1 || response["remote_failed"] != 1 {
		t.Fatalf("response = %#v, want one local move with one remote failure", response)
	}

	var inboxRows, spamRows, spamNullUIDs int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_folder_state WHERE message_id = ? AND folder_id = 'acc_inbox'`, msgID).Scan(&inboxRows); err != nil {
		t.Fatalf("count inbox rows: %v", err)
	}
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*), SUM(CASE WHEN remote_uid IS NULL THEN 1 ELSE 0 END) FROM message_folder_state WHERE message_id = ? AND folder_id = 'acc_spam'`, msgID).Scan(&spamRows, &spamNullUIDs); err != nil {
		t.Fatalf("count spam rows: %v", err)
	}
	if inboxRows != 0 || spamRows != 1 || spamNullUIDs != 1 {
		t.Fatalf("folder rows inbox=%d spam=%d spamNullUIDs=%d, want 0, 1, 1", inboxRows, spamRows, spamNullUIDs)
	}
}

func TestOutlookSpamActionDoesNotFallbackToLocalMoveWhenGraphFails(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Write().ExecContext(ctx, `INSERT OR IGNORE INTO users (id, username, username_normalized, name) VALUES ('default', 'default', 'default', 'Default')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, provider, email_address) VALUES ('acc', 'default', ?, 'user@outlook.com')`, providers.ProviderOutlook); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.UpsertFolders(ctx, []storage.UpsertFolderInput{
		{ID: "acc_inbox", AccountID: "acc", Name: "Inbox", RemoteID: "Inbox", ProviderRemoteID: "folder-inbox", Role: "inbox", Selectable: true},
		{ID: "acc_junk", AccountID: "acc", Name: "Junk Email", RemoteID: "Junk Email", ProviderRemoteID: "folder-junk", Role: "junk", Selectable: true},
	}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	ids, err := db.UpsertProviderSyncMessages(ctx, []storage.ProviderSyncMessage{{
		AccountID:         "acc",
		FolderID:          "acc_inbox",
		ProviderMessageID: "graph-message-1",
		InternetMessageID: "<outlook-spam@example.com>",
		Subject:           "Outlook spam",
		FromEmail:         "sender@example.com",
		DateSent:          time.Now(),
		DateReceived:      time.Now(),
		IsRead:            true,
	}})
	if err != nil {
		t.Fatalf("UpsertProviderSyncMessages() error = %v", err)
	}
	msgID := ids["graph-message-1"]
	if msgID == 0 {
		t.Fatal("provider message was not inserted")
	}

	h := &Handler{db: db, syncer: mail.NewSyncOrchestrator(db, nil, nil, nil)}
	body := `{"targets":[{"id":"` + strings.TrimSpace(strconv.FormatInt(msgID, 10)) + `"}],"folder_id":"acc_inbox"}`
	req := httptest.NewRequest(http.MethodPost, "/api/messages/spam", strings.NewReader(body))
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: "default"}))
	rec := httptest.NewRecorder()

	h.handleMarkMessagesSpam(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "microsoft oauth not configured") {
		t.Fatalf("body = %q, want Graph auth error", rec.Body.String())
	}

	var inboxRows, junkRows int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_folder_state WHERE message_id = ? AND folder_id = 'acc_inbox'`, msgID).Scan(&inboxRows); err != nil {
		t.Fatalf("count inbox rows: %v", err)
	}
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_folder_state WHERE message_id = ? AND folder_id = 'acc_junk'`, msgID).Scan(&junkRows); err != nil {
		t.Fatalf("count junk rows: %v", err)
	}
	if inboxRows != 1 || junkRows != 0 {
		t.Fatalf("folder rows inbox=%d junk=%d, want Outlook Graph failure to leave message in place", inboxRows, junkRows)
	}
}

// On a JMAP account, spam and not-spam move the message locally and queue
// the move for the server, as any other move is, instead of trying IMAP.
func TestJMAPSpamActionQueuesTheMove(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Write().ExecContext(ctx, `INSERT OR IGNORE INTO users (id, username, username_normalized, name) VALUES ('default', 'default', 'default', 'Default')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, provider, email_address) VALUES ('acc', 'default', ?, 'user@example.com')`, providers.ProviderJMAP); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.UpsertFolders(ctx, []storage.UpsertFolderInput{
		{ID: "acc_inbox", AccountID: "acc", RemoteID: "Inbox", ProviderRemoteID: "mb-inbox", Name: "Inbox", Role: "inbox", Selectable: true},
		{ID: "acc_junk", AccountID: "acc", RemoteID: "Junk", ProviderRemoteID: "mb-junk", Name: "Junk", Role: "junk", Selectable: true},
	}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	ids, err := db.UpsertProviderSyncMessages(ctx, []storage.ProviderSyncMessage{{AccountID: "acc", FolderID: "acc_inbox", ProviderMessageID: "e1",
		InternetMessageID: "<offer@example.com>", Subject: "A great offer", FromEmail: "sender@example.com", DateSent: time.Now(), DateReceived: time.Now()}})
	if err != nil || ids["e1"] == 0 {
		t.Fatalf("UpsertProviderSyncMessages() = %v, %v", ids, err)
	}
	msgID := ids["e1"]
	h := &Handler{db: db, syncer: mail.NewSyncOrchestrator(db, nil, nil, nil)}
	queued := func(from, to string) int {
		t.Helper()
		var count int
		if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_mutations WHERE message_id = ? AND kind = ? AND folder_id = ? AND destination_folder_id = ?`,
			msgID, storage.MessageMutationMove, from, to).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	mark := func(disposition spamDisposition, from, to string) {
		t.Helper()
		body := `{"targets":[{"id":"` + strconv.FormatInt(msgID, 10) + `"}],"folder_id":"` + from + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/messages/spam", strings.NewReader(body))
		req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: "default"}))
		rec := httptest.NewRecorder()
		h.handleMarkMessagesSpamState(rec, req, disposition)
		var response map[string]int
		if err := json.NewDecoder(rec.Body).Decode(&response); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, %v", disposition, rec.Code, err)
		}
		if response["messages"] != 1 || response["remote_failed"] != 0 || response["failed"] != 0 {
			t.Fatalf("%s: response = %#v, want one message moved", disposition, response)
		}
		var visibleIn string
		if err := db.Read().QueryRowContext(ctx, `SELECT folder_id FROM message_folder_state WHERE message_id = ? AND is_deleted = 0`, msgID).Scan(&visibleIn); err != nil || visibleIn != to {
			t.Fatalf("%s: message shows in %q, %v; want %s", disposition, visibleIn, err, to)
		}
	}
	mark(spamDispositionSpam, "acc_inbox", "acc_junk")
	if queued("acc_inbox", "acc_junk") != 1 {
		t.Fatal("the move into Junk was not queued")
	}
	// Undone before it was sent, there is nothing left to send.
	mark(spamDispositionNotSpam, "acc_junk", "acc_inbox")
	if queued("acc_inbox", "acc_junk")+queued("acc_junk", "acc_inbox") != 0 {
		t.Fatal("an undone spam move is still queued")
	}
	// Once the move reached the server, not-spam queues the way back.
	mark(spamDispositionSpam, "acc_inbox", "acc_junk")
	if _, err := db.Write().ExecContext(ctx, `DELETE FROM message_mutations WHERE message_id = ?`, msgID); err != nil {
		t.Fatal(err)
	}
	mark(spamDispositionNotSpam, "acc_junk", "acc_inbox")
	if queued("acc_junk", "acc_inbox") != 1 {
		t.Fatal("the move back to the inbox was not queued")
	}
}

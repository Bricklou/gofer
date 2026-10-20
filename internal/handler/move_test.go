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

// newTwoAccountMoveFixture has two accounts, each with an Inbox, an Archive
// and a Projects folder, and one archived message in each account.
func newTwoAccountMoveFixture(t *testing.T) (*Handler, *storage.DB, int64, int64) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Write().ExecContext(ctx, `INSERT OR IGNORE INTO users (id, username, username_normalized, name) VALUES ('default', 'default', 'default', 'Default')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var folders []storage.UpsertFolderInput
	var messages []storage.SyncMessage
	for i, account := range []string{"home", "work"} {
		if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, provider, email_address) VALUES (?, 'default', ?, ?)`, account, providers.ProviderIMAP, account+"@example.com"); err != nil {
			t.Fatalf("insert account: %v", err)
		}
		folders = append(folders,
			storage.UpsertFolderInput{ID: account + "_inbox", AccountID: account, RemoteID: "INBOX", Name: "Inbox", Role: "inbox", Selectable: true},
			storage.UpsertFolderInput{ID: account + "_archive", AccountID: account, RemoteID: "Archive", Name: "Archive", Role: "archive", Selectable: true},
			storage.UpsertFolderInput{ID: account + "_projects", AccountID: account, RemoteID: "Projects", Name: "Projects", Selectable: true},
		)
		messages = append(messages, storage.SyncMessage{
			AccountID: account,
			FolderID:  account + "_archive",
			RemoteUID: uint32(10 + i),
			MessageID: "<garden-" + account + "@example.com>",
			Subject:   "Garden notes",
			FromEmail: "neighbour@example.com",
			DateSent:  time.Now(),
			IsRead:    true,
		})
	}
	if err := db.UpsertFolders(ctx, folders); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	if err := db.UpsertSyncMessages(ctx, messages); err != nil {
		t.Fatalf("UpsertSyncMessages() error = %v", err)
	}
	homeID, err := db.GetMessageLocalIDByInternetIDInternal(ctx, "home", "<garden-home@example.com>")
	if err != nil || homeID == 0 {
		t.Fatalf("home message = %d, %v", homeID, err)
	}
	workID, err := db.GetMessageLocalIDByInternetIDInternal(ctx, "work", "<garden-work@example.com>")
	if err != nil || workID == 0 {
		t.Fatalf("work message = %d, %v", workID, err)
	}
	h := &Handler{db: db, syncer: mail.NewSyncOrchestrator(db, nil, nil, nil)}
	return h, db, homeID, workID
}

func postMove(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/messages/move", strings.NewReader(body))
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: "default"}))
	rec := httptest.NewRecorder()
	h.handleMoveMessages(rec, req)
	return rec
}

func liveFolderOf(t *testing.T, db *storage.DB, messageID int64) string {
	t.Helper()
	var folderID string
	if err := db.Read().QueryRowContext(context.Background(), `SELECT folder_id FROM message_folder_state WHERE message_id = ? AND is_deleted = 0`, messageID).Scan(&folderID); err != nil {
		t.Fatalf("load folder of %d: %v", messageID, err)
	}
	return folderID
}

func TestMoveToCombinedFolderUsesEachMessagesAccount(t *testing.T) {
	h, db, homeID, workID := newTwoAccountMoveFixture(t)
	body := `{"targets":[{"id":"` + strconv.FormatInt(homeID, 10) + `"},{"id":"` + strconv.FormatInt(workID, 10) + `"}],"folder_id":"inbox","source_folder_id":"archive"}`

	rec := postMove(t, h, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := liveFolderOf(t, db, homeID); got != "home_inbox" {
		t.Fatalf("home message folder = %q, want home_inbox", got)
	}
	if got := liveFolderOf(t, db, workID); got != "work_inbox" {
		t.Fatalf("work message folder = %q, want work_inbox", got)
	}
}

func TestMoveRefusesMessagesFromAnotherAccountAndMovesNothing(t *testing.T) {
	h, db, homeID, workID := newTwoAccountMoveFixture(t)
	body := `{"targets":[{"id":"` + strconv.FormatInt(homeID, 10) + `"},{"id":"` + strconv.FormatInt(workID, 10) + `"}],"folder_id":"home_projects","source_folder_id":"archive"}`

	rec := postMove(t, h, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := liveFolderOf(t, db, homeID); got != "home_archive" {
		t.Fatalf("home message folder = %q, want it left in home_archive", got)
	}
	if got := liveFolderOf(t, db, workID); got != "work_archive" {
		t.Fatalf("work message folder = %q, want it left in work_archive", got)
	}
}

func TestMoveRefusesFoldersThatAreNotPlaces(t *testing.T) {
	h, _, homeID, _ := newTwoAccountMoveFixture(t)
	body := `{"targets":[{"id":"` + strconv.FormatInt(homeID, 10) + `"}],"folder_id":"starred"}`

	if rec := postMove(t, h, body); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// Mail sent to oneself is in Sent and the Inbox as two copies. Moving it while
// looking at Sent moves the Sent copy, and the Inbox copy stays.
func TestMoveTakesTheCopyFromItsSourceFolder(t *testing.T) {
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Write().ExecContext(ctx, `INSERT OR IGNORE INTO users (id, username, username_normalized, name) VALUES ('default', 'default', 'default', 'Default');
		INSERT INTO accounts (id, user_id, provider, email_address) VALUES ('acc', 'default', 'jmap', 'robin@example.com')`); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if err := db.UpsertFolders(ctx, []storage.UpsertFolderInput{
		{ID: "acc_inbox", AccountID: "acc", RemoteID: "INBOX", ProviderRemoteID: "mb-inbox", Name: "Inbox", Role: "inbox", Selectable: true},
		{ID: "acc_sent", AccountID: "acc", RemoteID: "Sent", ProviderRemoteID: "mb-sent", Name: "Sent", Role: "sent", Selectable: true},
		{ID: "acc_archive", AccountID: "acc", RemoteID: "Archive", ProviderRemoteID: "mb-archive", Name: "Archive", Role: "archive", Selectable: true},
	}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	ids, err := db.UpsertProviderSyncMessages(ctx, []storage.ProviderSyncMessage{
		{AccountID: "acc", FolderID: "acc_sent", ProviderMessageID: "email-sent", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: time.Now()},
		{AccountID: "acc", FolderID: "acc_inbox", ProviderMessageID: "email-inbox", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: time.Now()},
	})
	if err != nil {
		t.Fatalf("UpsertProviderSyncMessages() error = %v", err)
	}
	h := &Handler{db: db, syncer: mail.NewSyncOrchestrator(db, nil, nil, nil)}
	msgID := ids["email-sent"]

	rec := postMove(t, h, `{"targets":[{"id":"`+strconv.FormatInt(msgID, 10)+`"}],"folder_id":"acc_archive","source_folder_id":"acc_sent"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	rows, err := db.Read().QueryContext(ctx, `SELECT folder_id, remote_message_id FROM message_folder_state WHERE message_id = ? AND is_deleted = 0 ORDER BY folder_id`, msgID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var folderID, providerID string
		if err := rows.Scan(&folderID, &providerID); err != nil {
			t.Fatal(err)
		}
		got = append(got, folderID+"="+providerID)
	}
	if want := []string{"acc_archive=email-sent", "acc_inbox=email-inbox"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("copies after the move = %v, want %v", got, want)
	}
}

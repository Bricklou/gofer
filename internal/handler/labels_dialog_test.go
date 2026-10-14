package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/auth"
	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func TestLabelsDialogMarksLabelsOnAllSomeOrNoneOfTheEmails(t *testing.T) {
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
	if err := db.UpsertFolders(ctx, []storage.UpsertFolderInput{{ID: "acc_inbox", AccountID: "acc", RemoteID: "INBOX", Name: "Inbox", Role: "inbox", Selectable: true}}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	var ids []int64
	for i, subject := range []string{"Bake sale", "Choir rehearsal"} {
		messageID := "<notice-" + strconv.Itoa(i) + "@example.com>"
		if err := db.UpsertSyncMessages(ctx, []storage.SyncMessage{{
			AccountID: "acc", FolderID: "acc_inbox", RemoteUID: uint32(i + 1), MessageID: messageID,
			Subject: subject, FromEmail: "village@example.com", DateSent: time.Now(), IsRead: true,
		}}); err != nil {
			t.Fatalf("UpsertSyncMessages() error = %v", err)
		}
		id, err := db.GetMessageLocalIDByInternetIDInternal(ctx, "acc", messageID)
		if err != nil || id == 0 {
			t.Fatalf("message id = %d, %v", id, err)
		}
		ids = append(ids, id)
	}
	label := func(id int64, name string) {
		t.Helper()
		if _, err := db.AddMessageLabel(ctx, id, "acc", storage.LabelInput{AccountID: "acc", Name: name, ProviderType: storage.LabelProviderLocal}); err != nil {
			t.Fatalf("AddMessageLabel(%s) error = %v", name, err)
		}
	}
	label(ids[0], "Village")
	label(ids[1], "Village")
	label(ids[0], "Volunteering")
	label(ids[0], "Garden")
	if err := db.RemoveMessageLabel(ctx, ids[0], "acc", "Garden"); err != nil {
		t.Fatalf("RemoveMessageLabel() error = %v", err)
	}

	h := &Handler{db: db}
	body := `{"targets":[{"id":"` + strconv.FormatInt(ids[0], 10) + `"},{"id":"` + strconv.FormatInt(ids[1], 10) + `"}],"folder_id":"acc_inbox"}`
	req := httptest.NewRequest(http.MethodPost, "/mail/labels-dialog", strings.NewReader(body))
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{ID: "default"}))
	rec := httptest.NewRecorder()

	h.handleLabelsDialog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	html := rec.Body.String()
	for name, want := range map[string]string{"Village": "all", "Volunteering": "some", "Garden": "none"} {
		row := regexp.MustCompile(`data-mail-label-option="` + name + `"[\s\S]*?data-state="([a-z]+)"`).FindStringSubmatch(html)
		if row == nil {
			t.Fatalf("label %q missing from the dialog", name)
		}
		if row[1] != want {
			t.Fatalf("label %q state = %q, want %q", name, row[1], want)
		}
	}
}

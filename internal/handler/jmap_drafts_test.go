package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// Discarding a JMAP account's draft in shared mode removes it locally without
// queueing an IMAP deletion, which a JMAP account has no server for.
func TestDiscardJMAPDraftQueuesNoIMAPDelete(t *testing.T) {
	h, db := newAccountOwnershipTestHandler(t)
	h.syncer = mail.NewSyncOrchestrator(db, nil, nil, nil)
	ctx := t.Context()
	if _, err := db.Write().ExecContext(ctx, `UPDATE accounts SET provider='jmap', imap_host='', smtp_host='' WHERE id='victim-account'`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertFolders(ctx, []storage.UpsertFolderInput{{ID: "victim-drafts", AccountID: "victim-account", RemoteID: "mb-drafts", Name: "Drafts", Role: "drafts", Selectable: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveDraftMessage(ctx, storage.DraftMessageInput{AccountID: "victim-account", FolderID: "victim-drafts", InternetMessageID: "<notes-1@example.test>", Subject: "Notes for Friday", FromEmail: "owner@example.com", Date: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"account_id": {"victim-account"}, "draft_id": {"<notes-1@example.test>"}}
	req := httptest.NewRequest(http.MethodPost, "/compose/draft/discard", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.handleDiscardComposeDraft(rec, ownerRequest(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("discard = %d %s", rec.Code, rec.Body.String())
	}
	var operations, drafts int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM imap_draft_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE internet_message_id='<notes-1@example.test>'`).Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	if operations != 0 || drafts != 0 {
		t.Fatalf("after discard: %d IMAP operations queued, %d drafts left", operations, drafts)
	}
}

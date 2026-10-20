package mail

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/message"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func TestUserAttachmentMIMEPartKeepsIdenticalSiblingOrder(t *testing.T) {
	p := storage.AttachmentRecoveryPart{Filename: "same.txt", ContentType: "text/plain; charset=utf-8", SizeBytes: 4}
	s := storage.AttachmentRecovery{PartIndex: 1, Parts: []storage.AttachmentRecoveryPart{p, p}}
	parts := []message.AttachmentMeta{{Filename: "same.txt", ContentType: "text/plain", Size: 4}, {Filename: "same.txt", ContentType: "text/plain", Size: 4}}
	if i, err := userAttachmentMIMEPart(s, parts); err != nil || i != 1 {
		t.Fatal("same-name sibling order lost", i, err)
	}
	parts = append(parts, parts[0])
	if _, err := userAttachmentMIMEPart(s, parts); err == nil {
		t.Fatal("changed duplicate layout accepted")
	}
	parts = parts[:2]
	parts[0].Size = 5
	if i, err := userAttachmentMIMEPart(s, parts); err != nil || i != 1 {
		t.Fatal("unique exact part not selected", i, err)
	}
	parts[1].ContentID = "different"
	if _, err := userAttachmentMIMEPart(s, parts); err == nil {
		t.Fatal("wrong content identity accepted")
	}
}

// Gmail and Outlook attachment ids belong to one message. When an
// attachment was listed from a copy that is gone, such as the Sent copy of
// mail sent to oneself, it is taken from the other copy's MIME.
func TestEnsureAttachmentFallsBackToTheOtherCopysMIME(t *testing.T) {
	f := newUserGmailFixture(t)
	const mime = "From: alice@mail.test\r\nTo: alice@mail.test\r\nSubject: Packing list\r\nMessage-ID: <self@mail.test>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b1\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nList attached.\r\n" +
		"--b1\r\nContent-Type: text/plain; name=list.txt\r\nContent-Disposition: attachment; filename=list.txt\r\n\r\nTent, stove\r\n--b1--\r\n"
	var attachmentRequests int
	api := f.api
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/users/me/messages/") && strings.Contains(r.URL.Path, "/attachments/"):
			attachmentRequests++
			http.Error(w, `{"error":{"code":404}}`, http.StatusNotFound)
		case r.URL.Path == "/users/me/messages/g-in" && r.URL.Query().Get("format") == "raw":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "g-in", "raw": base64.URLEncoding.EncodeToString([]byte(mime))})
		default:
			api.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	gmailAPIBaseURL = server.URL

	account := f.ids["alice"]
	var attachmentID int64
	f.local(t, "alice", func(db *storage.DB) error {
		ctx := t.Context()
		if err := db.UpsertFolders(ctx, []storage.UpsertFolderInput{
			{ID: account + "_inbox", AccountID: account, RemoteID: "INBOX", ProviderRemoteID: "INBOX", Name: "Inbox", Role: "inbox", Selectable: true},
			{ID: account + "_sent", AccountID: account, RemoteID: "SENT", ProviderRemoteID: "SENT", Name: "Sent", Role: "sent", Selectable: true},
		}); err != nil {
			return err
		}
		ids, err := db.UpsertProviderSyncMessages(ctx, []storage.ProviderSyncMessage{
			{AccountID: account, FolderID: account + "_sent", ProviderMessageID: "g-sent", InternetMessageID: "<self@mail.test>", Subject: "Packing list", DateSent: time.Now(), HasAttachments: true},
			{AccountID: account, FolderID: account + "_inbox", ProviderMessageID: "g-in", InternetMessageID: "<self@mail.test>", Subject: "Packing list", DateSent: time.Now(), HasAttachments: true},
		})
		if err != nil {
			return err
		}
		result, err := db.Write().ExecContext(ctx, `INSERT INTO attachments (message_id, filename, content_type, size_bytes, storage_path, provider_remote_id)
			VALUES (?, 'list.txt', 'text/plain', 11, '', 'att-from-sent')`, ids["g-sent"])
		if err != nil {
			return err
		}
		if attachmentID, err = result.LastInsertId(); err != nil {
			return err
		}
		_, err = db.MarkProviderMessageDeleted(ctx, account, "g-sent")
		return err
	})

	info, err := f.worker.EnsureAttachment(t.Context(), "alice", attachmentID)
	if err != nil {
		t.Fatalf("EnsureAttachment() error = %v", err)
	}
	if attachmentRequests == 0 {
		t.Fatal("did not try the attachment id first")
	}
	content, err := os.ReadFile(info.StoragePath)
	if err != nil || string(content) != "Tent, stove" {
		t.Fatalf("attachment = %q, %v", content, err)
	}
}

package jmap

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
)

const testRawMessage = "From: Robin Vale <robin@example.test>\r\n" +
	"To: sam@example.test\r\n" +
	"Subject: Lunch on Friday?\r\n" +
	"Message-ID: <lunch-1@example.test>\r\n" +
	"\r\n" +
	"Are you free around noon?\r\n"

func TestDownloadBlobFillsTemplate(t *testing.T) {
	var gotURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		if user, _, ok := r.BasicAuth(); !ok || user != "robin@example.test" {
			t.Errorf("missing basic auth")
		}
		w.Header().Set("Content-Type", ContentTypeMessage)
		_, _ = io.WriteString(w, testRawMessage)
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client(), Auth: BasicAuth("robin@example.test", "secret")}
	session := &Session{DownloadURL: server.URL + "/jmap/download/{accountId}/{blobId}/{name}?accept={type}"}
	body, err := client.DownloadBlob(context.Background(), session, "a1", "b1", "lunch notes.eml", ContentTypeMessage)
	if err != nil {
		t.Fatalf("DownloadBlob: %v", err)
	}
	defer body.Close()
	raw, _ := io.ReadAll(body)

	if want := "/jmap/download/a1/b1/lunch%20notes.eml?accept=message%2Frfc822"; gotURI != want {
		t.Fatalf("request URI = %q, want %q", gotURI, want)
	}
	if string(raw) != testRawMessage {
		t.Fatalf("body = %q", raw)
	}
}

func TestDownloadBlobNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blob not found", http.StatusNotFound)
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{DownloadURL: server.URL + "/download/{accountId}/{blobId}/{name}?accept={type}"}
	_, err := client.DownloadBlob(context.Background(), session, "a1", "gone", "", ContentTypeMessage)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want HTTPError 404", err)
	}
}

func TestExpandTemplate(t *testing.T) {
	values := map[string]string{"accountId": "a1", "blobId": "b/1", "name": "Q1 plan & notes.pdf", "type": "application/pdf"}
	got := expandTemplate("/dl/{accountId}/{blobId}/{name}?accept={type}&x={missing}", values)
	want := "/dl/a1/b%2F1/Q1%20plan%20%26%20notes.pdf?accept=application%2Fpdf&x="
	if got != want {
		t.Fatalf("expandTemplate = %q\nwant %q", got, want)
	}
}

func TestDownloadBlobLiveServer(t *testing.T) {
	client, session := liveSession(t)
	accountID := session.PrimaryAccount(CapabilityMail)
	queried, got, err := client.QueryAndGetEmails(context.Background(), session,
		EmailQuery{AccountID: accountID, Limit: 1, Sort: []Comparator{{Property: "receivedAt"}}},
		[]string{"id", "blobId", "subject", "size"})
	if err != nil {
		t.Fatalf("QueryAndGetEmails: %v", err)
	}
	if len(queried.IDs) == 0 {
		t.Skip("the test account has no email to download")
	}
	email := got.List[0]

	body, err := client.DownloadBlob(context.Background(), session, accountID, email.BlobID, "", ContentTypeMessage)
	if err != nil {
		t.Fatalf("DownloadBlob: %v", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 10<<20))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}

	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse raw message: %v", err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	t.Logf("email %s: %d bytes (size property %d), Subject: %q", email.ID, len(raw), email.Size, subject)
	if subject != email.Subject {
		t.Fatalf("raw Subject %q does not match Email/get subject %q", subject, email.Subject)
	}
	if int64(len(raw)) != email.Size {
		t.Fatalf("downloaded %d bytes, Email size is %d", len(raw), email.Size)
	}
}

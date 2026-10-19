package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUploadBlob(t *testing.T) {
	var gotPath, gotType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotType = r.URL.Path, r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = io.WriteString(w, `{"accountId": "a1", "blobId": "b-up", "type": "message/rfc822", "size": 9}`)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client()}
	session := &Session{UploadURL: server.URL + "/upload/{accountId}/", Core: CoreCapability{MaxSizeUpload: 20}}
	blob, err := client.UploadBlob(context.Background(), session, "a1", ContentTypeMessage, []byte("Subject: x"))
	if err != nil || blob != "b-up" {
		t.Fatalf("UploadBlob = %q, %v", blob, err)
	}
	if gotPath != "/upload/a1/" || gotType != ContentTypeMessage || gotBody != "Subject: x" {
		t.Fatalf("request = %s %s %q", gotPath, gotType, gotBody)
	}
	gotPath = ""
	if _, err := client.UploadBlob(context.Background(), session, "a1", ContentTypeMessage, make([]byte, 21)); !errors.Is(err, ErrUploadTooLarge) || gotPath != "" {
		t.Fatalf("upload over the limit = %v (sent: %v)", err, gotPath != "")
	}
}

func TestMatchIdentity(t *testing.T) {
	identities := []Identity{
		{ID: "i-domain", Email: "*@example.test"},
		{ID: "i-robin", Email: "Robin@Example.test"},
	}
	cases := map[string]string{
		"robin@example.test": "i-robin",
		"sam@example.test":   "i-domain",
		"sam@other.test":     "",
		"":                   "",
	}
	for address, want := range cases {
		identity, ok := MatchIdentity(identities, address)
		if got := map[bool]string{true: identity.ID}[ok]; got != want {
			t.Errorf("MatchIdentity(%q) = %q, want %q", address, got, want)
		}
	}
}

// sendServer answers Email/import and EmailSubmission/set; refuse makes the
// submission fail. It records every request's method calls.
func sendServer(t *testing.T, refuse bool, calls *[][][]json.RawMessage) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := decodeCalls(t, r)
		*calls = append(*calls, request)
		var name string
		_ = json.Unmarshal(request[0][0], &name)
		switch {
		case name == "Email/set":
			_, _ = io.WriteString(w, `{"methodResponses": [["Email/set", {"accountId": "a1", "destroyed": ["e-new"]}, "0"]], "sessionState": "s1"}`)
		case refuse:
			_, _ = io.WriteString(w, `{"methodResponses": [
			  ["Email/import", {"accountId": "a1", "created": {"draft": {"id": "e-new"}}}, "0"],
			  ["EmailSubmission/set", {"accountId": "a1", "notCreated": {"send": {"type": "forbiddenFrom", "description": "not your address"}}}, "1"]
			], "sessionState": "s1"}`)
		default:
			_, _ = io.WriteString(w, `{"methodResponses": [
			  ["Email/import", {"accountId": "a1", "created": {"draft": {"id": "e-new"}}}, "0"],
			  ["EmailSubmission/set", {"accountId": "a1", "created": {"send": {"id": "sub-1"}}}, "1"],
			  ["Email/set", {"accountId": "a1", "updated": {"e-new": null}}, "1"]
			], "sessionState": "s1"}`)
		}
	}))
}

var testOutgoing = Outgoing{
	BlobID: "b-up", IdentityID: "i-robin", DraftsID: "mb-drafts", SentID: "mb-sent",
	Envelope: Envelope{MailFrom: "robin@example.test", RcptTo: []string{"sam@example.test", "hidden@example.test"}},
}

func TestSendEmailImportsAndSubmitsInOneRequest(t *testing.T) {
	var calls [][][]json.RawMessage
	server := sendServer(t, false, &calls)
	defer server.Close()
	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	sent, err := client.SendEmail(context.Background(), session, "a1", testOutgoing)
	if err != nil || sent.EmailID != "e-new" || sent.SubmissionID != "sub-1" {
		t.Fatalf("SendEmail = %+v, %v", sent, err)
	}
	if len(calls) != 1 || len(calls[0]) != 2 {
		t.Fatalf("requests = %d, calls in first = %d; want one request with two calls", len(calls), len(calls[0]))
	}
	var importArgs, submitArgs map[string]any
	_ = json.Unmarshal(calls[0][0][1], &importArgs)
	_ = json.Unmarshal(calls[0][1][1], &submitArgs)
	draft := importArgs["emails"].(map[string]any)["draft"].(map[string]any)
	if draft["blobId"] != "b-up" || !reflect.DeepEqual(draft["mailboxIds"], map[string]any{"mb-drafts": true}) {
		t.Fatalf("import = %v", draft)
	}
	create := submitArgs["create"].(map[string]any)["send"].(map[string]any)
	wantEnvelope := map[string]any{
		"mailFrom": map[string]any{"email": "robin@example.test"},
		"rcptTo":   []any{map[string]any{"email": "sam@example.test"}, map[string]any{"email": "hidden@example.test"}},
	}
	if create["emailId"] != "#draft" || create["identityId"] != "i-robin" || !reflect.DeepEqual(create["envelope"], wantEnvelope) {
		t.Fatalf("submission = %v", create)
	}
	wantMove := map[string]any{"#send": map[string]any{"mailboxIds/mb-drafts": nil, "mailboxIds/mb-sent": true, "keywords/$draft": nil}}
	if !reflect.DeepEqual(submitArgs["onSuccessUpdateEmail"], wantMove) {
		t.Fatalf("onSuccessUpdateEmail = %v", submitArgs["onSuccessUpdateEmail"])
	}
}

func TestSendEmailRemovesTheDraftWhenRefused(t *testing.T) {
	var calls [][][]json.RawMessage
	server := sendServer(t, true, &calls)
	defer server.Close()
	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	_, err := client.SendEmail(context.Background(), session, "a1", testOutgoing)
	var refused *SubmissionError
	if !errors.As(err, &refused) || refused.Step != "submission" || refused.Type != "forbiddenFrom" {
		t.Fatalf("err = %v, want a refused submission", err)
	}
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want the send and the cleanup", len(calls))
	}
	var cleanup map[string]any
	_ = json.Unmarshal(calls[1][0][1], &cleanup)
	if !reflect.DeepEqual(cleanup["destroy"], []any{"e-new"}) {
		t.Fatalf("cleanup = %v", cleanup)
	}
}

// Sends a real message from GOFER_JMAP_TEST_USER to GOFER_JMAP_TEST_RECIPIENT
// (logging in as the recipient with GOFER_JMAP_TEST_RECIPIENT_PASSWORD) and
// checks it reached the recipient's Inbox and the sender's Sent.
func TestSendEmailLiveServer(t *testing.T) {
	recipient, recipientPassword := os.Getenv("GOFER_JMAP_TEST_RECIPIENT"), os.Getenv("GOFER_JMAP_TEST_RECIPIENT_PASSWORD")
	if recipient == "" || recipientPassword == "" {
		t.Skip("GOFER_JMAP_TEST_RECIPIENT and GOFER_JMAP_TEST_RECIPIENT_PASSWORD not set")
	}
	client, session := liveSession(t)
	ctx := context.Background()
	account := session.PrimaryAccount(CapabilityMail)
	sender := os.Getenv("GOFER_JMAP_TEST_USER")
	roles := liveMailboxRoles(t, client, session, account)
	identities, err := client.GetIdentities(ctx, session, account)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := MatchIdentity(identities.List, sender)
	if !ok {
		t.Fatalf("no identity for %s among %+v", sender, identities.List)
	}
	messageID := fmt.Sprintf("gofer-send-%d@gofertesting.test", time.Now().UnixNano())
	raw := "From: " + sender + "\r\nTo: " + recipient + "\r\nSubject: Sent through JMAP\r\nMessage-ID: <" + messageID + ">\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nA test message sent with EmailSubmission.\r\n"
	blob, err := client.UploadBlob(ctx, session, account, ContentTypeMessage, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	sent, err := client.SendEmail(ctx, session, account, Outgoing{
		BlobID: blob, IdentityID: identity.ID, DraftsID: roles["drafts"], SentID: roles["sent"],
		Envelope: Envelope{MailFrom: sender, RcptTo: []string{recipient}},
	})
	if err != nil {
		t.Fatalf("SendEmail: %v", err)
	}
	got, err := client.GetEmails(ctx, session, account, []string{sent.EmailID}, []string{"mailboxIds", "keywords"})
	if err != nil || len(got.List) != 1 {
		t.Fatalf("sent email: %+v, %v", got, err)
	}
	if !got.List[0].MailboxIDs[roles["sent"]] || got.List[0].MailboxIDs[roles["drafts"]] || got.List[0].Keywords[KeywordDraft] {
		t.Fatalf("sent email not moved to Sent: %+v", got.List[0])
	}

	other := &Client{Auth: BasicAuth(recipient, recipientPassword)}
	otherSession, err := other.Session(ctx, os.Getenv("GOFER_JMAP_TEST_URL"))
	if err != nil {
		t.Fatal(err)
	}
	otherAccount := otherSession.PrimaryAccount(CapabilityMail)
	inbox := liveMailboxRoles(t, other, otherSession, otherAccount)["inbox"]
	for attempt := 0; attempt < 20; attempt++ {
		_, emails, err := other.QueryAndGetEmails(ctx, otherSession, EmailQuery{AccountID: otherAccount, Filter: &EmailFilter{InMailbox: inbox},
			Sort: []Comparator{{Property: "receivedAt"}}, Limit: 10}, []string{"id", "messageId", "subject"})
		if err != nil {
			t.Fatal(err)
		}
		for _, email := range emails.List {
			if len(email.MessageID) > 0 && email.MessageID[0] == messageID {
				t.Logf("delivered to %s as %s (sent email %s, submission %s)", recipient, email.ID, sent.EmailID, sent.SubmissionID)
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("message %s did not reach %s's Inbox", messageID, recipient)
}

func liveMailboxRoles(t *testing.T, client *Client, session *Session, account string) map[string]string {
	t.Helper()
	mailboxes, err := client.GetMailboxes(context.Background(), session, account, nil)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, mailbox := range mailboxes.List {
		if role := strings.ToLower(mailbox.Role); role != "" {
			roles[role] = mailbox.ID
		}
	}
	return roles
}

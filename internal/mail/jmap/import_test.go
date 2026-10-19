package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// importServer answers every request with answer and keeps the calls.
func importServer(t *testing.T, answer string, calls *[][]json.RawMessage) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = decodeCalls(t, r)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(server.Close)
	return server
}

// ImportEmail stores the message in the mailbox asked for, with its keywords
// and its own received time.
func TestImportEmailKeepsKeywordsAndDate(t *testing.T) {
	var calls [][]json.RawMessage
	server := importServer(t, `{"methodResponses": [
	  ["Email/import", {"accountId": "a1", "created": {"email": {"id": "e1"}}}, "0"]
	], "sessionState": "s1"}`, &calls)
	received := time.Date(2024, 3, 5, 9, 30, 0, 0, time.FixedZone("ART", -3*3600))
	imported, err := (&Client{HTTP: server.Client()}).ImportEmail(context.Background(), &Session{APIURL: server.URL}, "a1", "b1", "mb-inbox", []string{KeywordSeen, KeywordFlagged}, received)
	if err != nil || *imported != (Imported{ID: "e1"}) {
		t.Fatalf("ImportEmail = %+v, %v", imported, err)
	}
	var args map[string]any
	_ = json.Unmarshal(calls[0][1], &args)
	email := args["emails"].(map[string]any)["email"].(map[string]any)
	if email["blobId"] != "b1" || !reflect.DeepEqual(email["mailboxIds"], map[string]any{"mb-inbox": true}) ||
		!reflect.DeepEqual(email["keywords"], map[string]any{"$seen": true, "$flagged": true}) ||
		email["receivedAt"] != "2024-03-05T12:30:00Z" {
		t.Fatalf("import = %v", email)
	}
}

// A message the server already has is the copy it has; any other refusal is
// an *ImportError.
func TestImportEmailReportsWhatHappened(t *testing.T) {
	var calls [][]json.RawMessage
	existing := importServer(t, `{"methodResponses": [
	  ["Email/import", {"accountId": "a1", "notCreated": {"email": {"type": "alreadyExists", "existingId": "e0"}}}, "0"]
	], "sessionState": "s1"}`, &calls)
	imported, err := (&Client{HTTP: existing.Client()}).ImportEmail(context.Background(), &Session{APIURL: existing.URL}, "a1", "b1", "mb", nil, time.Time{})
	if err != nil || *imported != (Imported{ID: "e0", Existed: true}) {
		t.Fatalf("existing = %+v, %v", imported, err)
	}
	var args map[string]any
	_ = json.Unmarshal(calls[0][1], &args)
	if _, dated := args["emails"].(map[string]any)["email"].(map[string]any)["receivedAt"]; dated {
		t.Fatal("a zero date was sent")
	}
	refused := importServer(t, `{"methodResponses": [
	  ["Email/import", {"accountId": "a1", "notCreated": {"email": {"type": "invalidEmail"}}}, "0"]
	], "sessionState": "s1"}`, &calls)
	imported, err = (&Client{HTTP: refused.Client()}).ImportEmail(context.Background(), &Session{APIURL: refused.URL}, "a1", "b1", "mb", nil, time.Time{})
	var importErr *ImportError
	if imported != nil || !errors.As(err, &importErr) || importErr.Type != "invalidEmail" {
		t.Fatalf("refused = %+v, %v", imported, err)
	}
}

func TestCreateMailbox(t *testing.T) {
	var calls [][]json.RawMessage
	server := importServer(t, `{"methodResponses": [
	  ["Mailbox/set", {"accountId": "a1", "created": {"mailbox": {"id": "mb2"}}}, "0"]
	], "sessionState": "s1"}`, &calls)
	client := &Client{HTTP: server.Client()}
	for _, parent := range []string{"", "mb1"} {
		id, err := client.CreateMailbox(context.Background(), &Session{APIURL: server.URL}, "a1", "Receipts", parent)
		if err != nil || id != "mb2" {
			t.Fatalf("CreateMailbox = %q, %v", id, err)
		}
		var args map[string]any
		_ = json.Unmarshal(calls[0][1], &args)
		mailbox := args["create"].(map[string]any)["mailbox"].(map[string]any)
		var want any
		if parent != "" {
			want = parent
		}
		if mailbox["name"] != "Receipts" || mailbox["parentId"] != want {
			t.Fatalf("create = %v", mailbox)
		}
	}
	refused := importServer(t, `{"methodResponses": [
	  ["Mailbox/set", {"accountId": "a1", "notCreated": {"mailbox": {"type": "forbidden"}}}, "0"]
	], "sessionState": "s1"}`, &calls)
	_, err := (&Client{HTTP: refused.Client()}).CreateMailbox(context.Background(), &Session{APIURL: refused.URL}, "a1", "Receipts", "")
	var importErr *ImportError
	if !errors.As(err, &importErr) || importErr.Type != "forbidden" {
		t.Fatalf("refused = %v", err)
	}
}

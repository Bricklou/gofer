package jmap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testEmailJSON = `{
  "id": "e1", "blobId": "b1", "threadId": "t1",
  "mailboxIds": {"mb1": true}, "keywords": {"$seen": true},
  "size": 2048, "receivedAt": "2026-03-02T09:15:00Z",
  "messageId": ["lunch-1@example.test"], "inReplyTo": null, "references": null,
  "from": [{"name": "Robin Vale", "email": "robin@example.test"}],
  "to": [{"name": null, "email": "sam@example.test"}],
  "cc": null, "bcc": null, "replyTo": null,
  "subject": "Lunch on Friday?", "sentAt": "2026-03-02T10:14:58+01:00",
  "hasAttachment": false, "preview": "Are you free around noon?"
}`

// decodeCalls reads the methodCalls of a request as [name, args, id] parts.
func decodeCalls(t *testing.T, r *http.Request) [][]json.RawMessage {
	t.Helper()
	var request struct {
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatalf("request is not JSON: %v", err)
	}
	return request.MethodCalls
}

func TestQueryAndGetEmailsUsesResultReference(t *testing.T) {
	var calls [][]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = decodeCalls(t, r)
		_, _ = w.Write([]byte(`{"methodResponses": [
		  ["Email/query", {"accountId": "a1", "queryState": "q1", "canCalculateChanges": true,
		                   "position": 0, "ids": ["e1"], "total": 1}, "0"],
		  ["Email/get", {"accountId": "a1", "state": "es1", "notFound": [], "list": [` + testEmailJSON + `]}, "1"]
		], "sessionState": "s1"}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	query := EmailQuery{
		AccountID:      "a1",
		Filter:         &EmailFilter{InMailbox: "mb1"},
		Sort:           []Comparator{{Property: "receivedAt", IsAscending: false}},
		Limit:          50,
		CalculateTotal: true,
	}
	queried, got, err := client.QueryAndGetEmails(context.Background(), session, query, EmailSummaryProperties)
	if err != nil {
		t.Fatalf("QueryAndGetEmails: %v", err)
	}

	// Both calls go out in one request, and the get points at the query.
	if len(calls) != 2 {
		t.Fatalf("got %d method calls, want 2", len(calls))
	}
	var queryArgs map[string]any
	_ = json.Unmarshal(calls[0][1], &queryArgs)
	if queryArgs["filter"].(map[string]any)["inMailbox"] != "mb1" || queryArgs["limit"] != float64(50) {
		t.Fatalf("query args = %v", queryArgs)
	}
	var getArgs map[string]any
	_ = json.Unmarshal(calls[1][1], &getArgs)
	if _, plain := getArgs["ids"]; plain {
		t.Fatalf("get sent plain ids alongside the reference: %v", getArgs)
	}
	ref, _ := getArgs["#ids"].(map[string]any)
	if ref["resultOf"] != "0" || ref["name"] != "Email/query" || ref["path"] != "/ids" {
		t.Fatalf("#ids = %v", getArgs["#ids"])
	}

	if queried.Total != 1 || queried.QueryState != "q1" || len(queried.IDs) != 1 {
		t.Fatalf("query result = %+v", queried)
	}
	if got.State != "es1" || len(got.List) != 1 {
		t.Fatalf("get result = %+v", got)
	}
	email := got.List[0]
	if email.Subject != "Lunch on Friday?" || email.From[0].Email != "robin@example.test" || !email.Keywords[KeywordSeen] || !email.MailboxIDs["mb1"] {
		t.Fatalf("email = %+v", email)
	}
	if want := time.Date(2026, 3, 2, 9, 15, 0, 0, time.UTC); !email.ReceivedAt.Equal(want) {
		t.Fatalf("receivedAt = %v, want %v", email.ReceivedAt, want)
	}
	// sentAt keeps the sender's offset; it is the same instant as 09:14:58Z.
	if want := time.Date(2026, 3, 2, 9, 14, 58, 0, time.UTC); !email.SentAt.Equal(want) {
		t.Fatalf("sentAt = %v, want %v", email.SentAt, want)
	}
	if email.InReplyTo != nil || email.To[0].Name != "" {
		t.Fatalf("null fields: inReplyTo=%v to=%+v", email.InReplyTo, email.To)
	}
}

func TestGetEmailsSendsIDsAndProperties(t *testing.T) {
	var calls [][]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = decodeCalls(t, r)
		_, _ = w.Write([]byte(`{"methodResponses": [["Email/get",
		  {"accountId": "a1", "state": "es1", "notFound": ["gone"], "list": [{"id": "e1", "keywords": {}, "mailboxIds": {"mb2": true}}]}, "0"]],
		  "sessionState": "s1"}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	got, err := client.GetEmails(context.Background(), session, "a1", []string{"e1", "gone"}, []string{"keywords", "mailboxIds"})
	if err != nil {
		t.Fatalf("GetEmails: %v", err)
	}
	var args struct {
		IDs        []string `json:"ids"`
		Properties []string `json:"properties"`
	}
	_ = json.Unmarshal(calls[0][1], &args)
	if len(args.IDs) != 2 || len(args.Properties) != 2 || args.Properties[0] != "keywords" {
		t.Fatalf("args = %+v", args)
	}
	if len(got.NotFound) != 1 || got.NotFound[0] != "gone" || !got.List[0].MailboxIDs["mb2"] {
		t.Fatalf("result = %+v", got)
	}
}

func TestQueryAndGetEmailsLiveServer(t *testing.T) {
	client, session := liveSession(t)
	accountID := session.PrimaryAccount(CapabilityMail)
	mailboxes, err := client.GetMailboxes(context.Background(), session, accountID, nil)
	if err != nil {
		t.Fatalf("GetMailboxes: %v", err)
	}
	var inbox string
	for _, mailbox := range mailboxes.List {
		if mailbox.Role == "inbox" {
			inbox = mailbox.ID
		}
	}
	if inbox == "" {
		t.Fatal("no inbox")
	}

	query := EmailQuery{
		AccountID:      accountID,
		Filter:         &EmailFilter{InMailbox: inbox},
		Sort:           []Comparator{{Property: "receivedAt", IsAscending: false}},
		Limit:          20,
		CalculateTotal: true,
	}
	queried, got, err := client.QueryAndGetEmails(context.Background(), session, query, EmailSummaryProperties)
	if err != nil {
		t.Fatalf("QueryAndGetEmails: %v", err)
	}
	if len(got.List) != len(queried.IDs) {
		t.Fatalf("query returned %d ids but get returned %d emails", len(queried.IDs), len(got.List))
	}
	t.Logf("inbox total=%d queryState=%s emailState=%s", queried.Total, queried.QueryState, got.State)
	for _, email := range got.List {
		var from string
		if len(email.From) > 0 {
			from = email.From[0].Email
		}
		t.Logf("%s  %s  from=%s  seen=%v  subject=%q", email.ID, email.ReceivedAt.Format(time.RFC3339), from, email.Keywords[KeywordSeen], email.Subject)
	}
}

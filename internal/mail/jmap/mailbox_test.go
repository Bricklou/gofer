package jmap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mailboxGetServer answers Mailbox/get with two invented mailboxes and
// records the arguments of the call it received.
func mailboxGetServer(t *testing.T, gotArgs *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Using       []string            `json:"using"`
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &request); err != nil || len(request.MethodCalls) != 1 {
			t.Errorf("unexpected request %s", raw)
			return
		}
		var name string
		_ = json.Unmarshal(request.MethodCalls[0][0], &name)
		if name != "Mailbox/get" {
			t.Errorf("method = %q, want Mailbox/get", name)
		}
		_ = json.Unmarshal(request.MethodCalls[0][1], gotArgs)
		_, _ = w.Write([]byte(`{"methodResponses": [["Mailbox/get", {
		  "accountId": "a1", "state": "m7", "notFound": [],
		  "list": [
		    {"id": "mb1", "name": "Inbox", "parentId": null, "role": "inbox", "sortOrder": 1,
		     "totalEmails": 12, "unreadEmails": 3, "totalThreads": 10, "unreadThreads": 2,
		     "myRights": {"mayReadItems": true, "mayAddItems": true, "maySetSeen": true},
		     "isSubscribed": true},
		    {"id": "mb2", "name": "Receipts", "parentId": "mb1", "role": null, "sortOrder": 0,
		     "totalEmails": 4, "unreadEmails": 0, "totalThreads": 4, "unreadThreads": 0,
		     "myRights": {"mayReadItems": true}, "isSubscribed": false}
		  ]}, "0"]], "sessionState": "s1"}`))
	}))
}

func TestGetMailboxesAll(t *testing.T) {
	var gotArgs map[string]any
	server := mailboxGetServer(t, &gotArgs)
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	result, err := client.GetMailboxes(context.Background(), session, "a1", nil)
	if err != nil {
		t.Fatalf("GetMailboxes: %v", err)
	}

	// nil ids must go out as null, which means "all mailboxes".
	if ids, ok := gotArgs["ids"]; !ok || ids != nil {
		t.Fatalf("ids = %#v (present %v), want null", ids, ok)
	}
	if gotArgs["accountId"] != "a1" {
		t.Fatalf("accountId = %v", gotArgs["accountId"])
	}

	if result.State != "m7" || len(result.List) != 2 {
		t.Fatalf("result = %+v", result)
	}
	inbox, child := result.List[0], result.List[1]
	if inbox.Role != "inbox" || inbox.ParentID != "" || inbox.UnreadEmails != 3 || !inbox.MyRights.MaySetSeen {
		t.Fatalf("inbox = %+v", inbox)
	}
	// null role decodes to "", and parentId links the child to the inbox.
	if child.Role != "" || child.ParentID != "mb1" || child.IsSubscribed {
		t.Fatalf("child = %+v", child)
	}
}

func TestGetMailboxesEmptyIDsAsksForStateOnly(t *testing.T) {
	var gotArgs map[string]any
	server := mailboxGetServer(t, &gotArgs)
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	if _, err := client.GetMailboxes(context.Background(), session, "a1", []string{}); err != nil {
		t.Fatalf("GetMailboxes: %v", err)
	}
	if ids, ok := gotArgs["ids"].([]any); !ok || len(ids) != 0 {
		t.Fatalf("ids = %#v, want []", gotArgs["ids"])
	}
}

func TestGetMailboxesLiveServer(t *testing.T) {
	client, session := liveSession(t)
	result, err := client.GetMailboxes(context.Background(), session, session.PrimaryAccount(CapabilityMail), nil)
	if err != nil {
		t.Fatalf("GetMailboxes: %v", err)
	}
	var hasInbox bool
	for _, mailbox := range result.List {
		t.Logf("%-12s role=%-8s parent=%-6s total=%d unread=%d", mailbox.Name, mailbox.Role, mailbox.ParentID, mailbox.TotalEmails, mailbox.UnreadEmails)
		hasInbox = hasInbox || mailbox.Role == "inbox"
	}
	if !hasInbox {
		t.Fatalf("no mailbox with role inbox among %d", len(result.List))
	}
	t.Logf("state=%s", result.State)
}

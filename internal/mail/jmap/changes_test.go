package jmap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestGetMailboxChanges(t *testing.T) {
	var args map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.Unmarshal(decodeCalls(t, r)[0][1], &args)
		_, _ = w.Write([]byte(`{"methodResponses": [["Mailbox/changes", {
		  "accountId": "a1", "oldState": "m1", "newState": "m2", "hasMoreChanges": false,
		  "created": ["mb9"], "updated": ["mb1"], "destroyed": [],
		  "updatedProperties": ["totalEmails", "unreadEmails"]}, "0"]], "sessionState": "s1"}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	changes, err := client.GetMailboxChanges(context.Background(), session, "a1", "m1", 0)
	if err != nil {
		t.Fatalf("GetMailboxChanges: %v", err)
	}
	if args["sinceState"] != "m1" || args["accountId"] != "a1" {
		t.Fatalf("args = %v", args)
	}
	if _, sent := args["maxChanges"]; sent {
		t.Fatalf("maxChanges 0 should be left out: %v", args)
	}
	if changes.NewState != "m2" || len(changes.Created) != 1 || changes.Updated[0] != "mb1" {
		t.Fatalf("changes = %+v", changes)
	}
	if len(changes.UpdatedProperties) != 2 || changes.UpdatedProperties[0] != "totalEmails" {
		t.Fatalf("updatedProperties = %v", changes.UpdatedProperties)
	}
}

func TestGetMailboxChangesNullUpdatedProperties(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"methodResponses": [["Mailbox/changes", {
		  "accountId": "a1", "oldState": "m1", "newState": "m2", "hasMoreChanges": true,
		  "created": [], "updated": ["mb1"], "destroyed": ["mb4"],
		  "updatedProperties": null}, "0"]], "sessionState": "s1"}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	changes, err := client.GetMailboxChanges(context.Background(), session, "a1", "m1", 50)
	if err != nil {
		t.Fatalf("GetMailboxChanges: %v", err)
	}
	// null means "anything may have changed", which callers must tell apart
	// from a counters-only update.
	if changes.UpdatedProperties != nil || !changes.HasMoreChanges || changes.Destroyed[0] != "mb4" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestGetMailboxChangesCannotCalculate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"methodResponses": [["error", {"type": "cannotCalculateChanges"}, "0"]], "sessionState": "s1"}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	_, err := client.GetMailboxChanges(context.Background(), session, "a1", "ancient", 0)
	if !IsCannotCalculateChanges(err) {
		t.Fatalf("err = %v, want cannotCalculateChanges", err)
	}
}

// Asks for changes since the current state, which must be none. Set
// GOFER_JMAP_TEST_SINCE_STATE to an older state to see real changes.
func TestGetMailboxChangesLiveServer(t *testing.T) {
	client, session := liveSession(t)
	accountID := session.PrimaryAccount(CapabilityMail)
	since := os.Getenv("GOFER_JMAP_TEST_SINCE_STATE")
	if since == "" {
		current, err := client.GetMailboxes(context.Background(), session, accountID, []string{})
		if err != nil {
			t.Fatalf("GetMailboxes: %v", err)
		}
		since = current.State
	}
	changes, err := client.GetMailboxChanges(context.Background(), session, accountID, since, 0)
	if err != nil {
		t.Fatalf("GetMailboxChanges: %v", err)
	}
	t.Logf("since=%s new=%s more=%v created=%v updated=%v destroyed=%v updatedProperties=%v",
		since, changes.NewState, changes.HasMoreChanges, changes.Created, changes.Updated, changes.Destroyed, changes.UpdatedProperties)
	if os.Getenv("GOFER_JMAP_TEST_SINCE_STATE") == "" && len(changes.Created)+len(changes.Updated)+len(changes.Destroyed) != 0 {
		t.Fatalf("changes since the current state should be empty")
	}
}

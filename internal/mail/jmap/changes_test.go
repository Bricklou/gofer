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

func TestGetEmailChanges(t *testing.T) {
	var name string
	var args map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeCalls(t, r)[0]
		_ = json.Unmarshal(call[0], &name)
		_ = json.Unmarshal(call[1], &args)
		_, _ = w.Write([]byte(`{"methodResponses": [["Email/changes", {
		  "accountId": "a1", "oldState": "es1", "newState": "es2", "hasMoreChanges": false,
		  "created": ["e2"], "updated": ["e1"], "destroyed": ["e0"]}, "0"]], "sessionState": "s1"}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	changes, err := client.GetEmailChanges(context.Background(), session, "a1", "es1", 100)
	if err != nil {
		t.Fatalf("GetEmailChanges: %v", err)
	}
	if name != "Email/changes" || args["sinceState"] != "es1" || args["maxChanges"] != float64(100) {
		t.Fatalf("call = %s %v", name, args)
	}
	if changes.NewState != "es2" || changes.Created[0] != "e2" || changes.Updated[0] != "e1" || changes.Destroyed[0] != "e0" {
		t.Fatalf("changes = %+v", changes)
	}
}

// Like the Mailbox one; with GOFER_JMAP_TEST_SINCE_STATE set it also shows
// the current keywords and mailboxes of each updated email.
func TestGetEmailChangesLiveServer(t *testing.T) {
	client, session := liveSession(t)
	accountID := session.PrimaryAccount(CapabilityMail)
	since := os.Getenv("GOFER_JMAP_TEST_SINCE_STATE")
	if since == "" {
		current, err := client.GetEmails(context.Background(), session, accountID, []string{}, nil)
		if err != nil {
			t.Fatalf("GetEmails: %v", err)
		}
		since = current.State
	}
	changes, err := client.GetEmailChanges(context.Background(), session, accountID, since, 0)
	if err != nil {
		t.Fatalf("GetEmailChanges: %v", err)
	}
	t.Logf("since=%s new=%s more=%v created=%v updated=%v destroyed=%v",
		since, changes.NewState, changes.HasMoreChanges, changes.Created, changes.Updated, changes.Destroyed)
	if os.Getenv("GOFER_JMAP_TEST_SINCE_STATE") == "" {
		if len(changes.Created)+len(changes.Updated)+len(changes.Destroyed) != 0 {
			t.Fatalf("changes since the current state should be empty")
		}
		return
	}
	if len(changes.Updated) > 0 {
		updated, err := client.GetEmails(context.Background(), session, accountID, changes.Updated, []string{"keywords", "mailboxIds"})
		if err != nil {
			t.Fatalf("GetEmails: %v", err)
		}
		for _, email := range updated.List {
			t.Logf("updated %s keywords=%v mailboxIds=%v", email.ID, email.Keywords, email.MailboxIDs)
		}
	}
}

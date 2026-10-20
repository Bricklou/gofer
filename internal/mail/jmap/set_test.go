package jmap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPatchesTouchOnlyWhatChanges(t *testing.T) {
	cases := []struct {
		patch PatchObject
		want  string
	}{
		{KeywordPatch(KeywordSeen, true), `{"keywords/$seen":true}`},
		{KeywordPatch(KeywordFlagged, false), `{"keywords/$flagged":null}`},
		{MovePatch("mb-in", "mb-arch"), `{"mailboxIds/mb-arch":true,"mailboxIds/mb-in":null}`},
		{MovePatch("", "mb-arch"), `{"mailboxIds/mb-arch":true}`},
		{KeywordPatch("work/2026~x", true), `{"keywords/work~12026~0x":true}`},
	}
	for _, tc := range cases {
		got, _ := json.Marshal(tc.patch)
		if string(got) != tc.want {
			t.Errorf("patch = %s, want %s", got, tc.want)
		}
	}
}

func TestSetEmailsReportsEachEmail(t *testing.T) {
	var args map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeCalls(t, r)[0]
		var name string
		_ = json.Unmarshal(call[0], &name)
		_ = json.Unmarshal(call[1], &args)
		if name != "Email/set" {
			t.Errorf("method = %s", name)
		}
		_, _ = w.Write([]byte(`{"methodResponses": [["Email/set", {
		  "accountId": "a1", "oldState": "s1", "newState": "s2",
		  "updated": {"e1": null}, "destroyed": ["e3"],
		  "notUpdated": {"e2": {"type": "notFound"}},
		  "notDestroyed": {"e4": {"type": "forbidden", "description": "read-only mailbox"}}}, "0"]], "sessionState": "s1"}`))
	}))
	defer server.Close()

	client := &Client{HTTP: server.Client()}
	session := &Session{APIURL: server.URL + "/jmap/"}
	result, err := client.SetEmails(context.Background(), session, EmailSet{
		AccountID: "a1",
		Update: map[string]PatchObject{
			"e1": KeywordPatch(KeywordSeen, true),
			"e2": MovePatch("mb-in", "mb-arch"),
		},
		Destroy: []string{"e3", "e4"},
	})
	if err != nil {
		t.Fatalf("SetEmails: %v", err)
	}
	wantArgs := map[string]any{
		"accountId": "a1",
		"update": map[string]any{
			"e1": map[string]any{"keywords/$seen": true},
			"e2": map[string]any{"mailboxIds/mb-arch": true, "mailboxIds/mb-in": nil},
		},
		"destroy": []any{"e3", "e4"},
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v\nwant %#v", args, wantArgs)
	}
	if _, ok := result.Updated["e1"]; !ok || result.NewState != "s2" || len(result.Destroyed) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.NotUpdated["e2"].Type != "notFound" {
		t.Fatalf("notUpdated = %+v", result.NotUpdated)
	}
	if failure := result.NotDestroyed["e4"]; failure.Type != "forbidden" || failure.Error() != "jmap: forbidden: read-only mailbox" {
		t.Fatalf("notDestroyed = %+v", result.NotDestroyed)
	}
}

// Flags and unflags the oldest email on a real server, leaving it as it was.
func TestSetEmailsLiveServer(t *testing.T) {
	client, session := liveSession(t)
	accountID := session.PrimaryAccount(CapabilityMail)
	queried, got, err := client.QueryAndGetEmails(context.Background(), session,
		EmailQuery{AccountID: accountID, Limit: 1, Sort: []Comparator{{Property: "receivedAt", IsAscending: true}}},
		[]string{"id", "keywords"})
	if err != nil {
		t.Fatal(err)
	}
	if len(queried.IDs) == 0 {
		t.Skip("the test account has no email")
	}
	email := got.List[0]
	wasFlagged := email.Keywords[KeywordFlagged]
	flag := func(on bool) {
		t.Helper()
		result, err := client.SetEmails(context.Background(), session, EmailSet{
			AccountID: accountID,
			Update:    map[string]PatchObject{email.ID: KeywordPatch(KeywordFlagged, on)},
		})
		if err != nil {
			t.Fatalf("SetEmails: %v", err)
		}
		if _, ok := result.Updated[email.ID]; !ok {
			t.Fatalf("not updated: %+v", result.NotUpdated)
		}
	}
	defer flag(wasFlagged)
	flag(!wasFlagged)
	after, err := client.GetEmails(context.Background(), session, accountID, []string{email.ID}, []string{"keywords"})
	if err != nil {
		t.Fatal(err)
	}
	if after.List[0].Keywords[KeywordFlagged] == wasFlagged {
		t.Fatalf("flag did not change: %v", after.List[0].Keywords)
	}
	t.Logf("email %s flagged %v -> %v and back", email.ID, wasFlagged, !wasFlagged)
}

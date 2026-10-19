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
	"testing"
	"time"
)

// FindDrafts reads the Drafts mailbox page by page, within the server's get
// limit, and keeps the drafts with exactly the Message-ID asked for.
func TestFindDraftsReadsTheDraftsMailbox(t *testing.T) {
	pages := map[int]string{
		0: `[["Email/query", {"accountId": "a1", "ids": ["d1", "d2"]}, "0"],
		     ["Email/get", {"accountId": "a1", "state": "s1", "list": [
		       {"id": "d1", "messageId": ["notes-1@example.test"], "header:X-Gofer-Draft-Revision:asText": " r2 "},
		       {"id": "d2", "messageId": ["notes-1@example.test.other"]}]}, "1"]]`,
		2: `[["Email/query", {"accountId": "a1", "ids": ["d3"]}, "0"],
		     ["Email/get", {"accountId": "a1", "state": "s1", "list": [
		       {"id": "d3", "messageId": ["notes-1@example.test"], "header:X-Gofer-Draft-Revision:asText": null}]}, "1"]]`,
	}
	var positions []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var query EmailQuery
		_ = json.Unmarshal(decodeCalls(t, r)[0][1], &query)
		if query.Filter == nil || query.Filter.InMailbox != "mb-drafts" || query.Limit != 2 {
			t.Errorf("query = %+v", query)
		}
		positions = append(positions, query.Position)
		_, _ = io.WriteString(w, `{"methodResponses": `+pages[query.Position]+`, "sessionState": "s1"}`)
	}))
	defer server.Close()
	session := &Session{APIURL: server.URL, Core: CoreCapability{MaxObjectsInGet: 2}}
	drafts, err := (&Client{HTTP: server.Client()}).FindDrafts(context.Background(), session, "a1", "mb-drafts", "<notes-1@example.test>")
	if err != nil {
		t.Fatal(err)
	}
	want := []Draft{{ID: "d1", MessageID: "<notes-1@example.test>", Revision: "r2"}, {ID: "d3", MessageID: "<notes-1@example.test>"}}
	if !reflect.DeepEqual(drafts, want) || !reflect.DeepEqual(positions, []int{0, 2}) {
		t.Fatalf("drafts = %+v after pages %v", drafts, positions)
	}
}

// draftServer answers SaveDraft's request; notCreated refuses the import and
// notDestroyed refuses removing the earlier version.
func draftServer(t *testing.T, answer string, calls *[][][]json.RawMessage) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, decodeCalls(t, r))
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSaveDraftReplacesTheEarlierVersionInOneRequest(t *testing.T) {
	var calls [][][]json.RawMessage
	server := draftServer(t, `{"methodResponses": [
	  ["Email/import", {"accountId": "a1", "created": {"draft": {"id": "d2"}}}, "0"],
	  ["Email/set", {"accountId": "a1", "destroyed": ["d1"], "notDestroyed": {"d0": {"type": "notFound"}}}, "1"]
	], "sessionState": "s1"}`, &calls)
	client := &Client{HTTP: server.Client()}
	id, err := client.SaveDraft(context.Background(), &Session{APIURL: server.URL}, "a1", "b-new", "mb-drafts", []string{"d1", "d0"})
	if err != nil || id != "d2" {
		t.Fatalf("SaveDraft = %q, %v", id, err)
	}
	if len(calls) != 1 || len(calls[0]) != 2 {
		t.Fatalf("want one request with two calls, got %d requests", len(calls))
	}
	var imported map[string]any
	_ = json.Unmarshal(calls[0][0][1], &imported)
	draft := imported["emails"].(map[string]any)["draft"].(map[string]any)
	if draft["blobId"] != "b-new" || !reflect.DeepEqual(draft["mailboxIds"], map[string]any{"mb-drafts": true}) ||
		!reflect.DeepEqual(draft["keywords"], map[string]any{"$draft": true, "$seen": true}) {
		t.Fatalf("import = %v", draft)
	}
	var removed EmailSet
	_ = json.Unmarshal(calls[0][1][1], &removed)
	if !reflect.DeepEqual(removed.Destroy, []string{"d1", "d0"}) {
		t.Fatalf("destroy = %v", removed.Destroy)
	}
}

func TestSaveDraftReportsWhatHappened(t *testing.T) {
	var calls [][][]json.RawMessage
	refused := draftServer(t, `{"methodResponses": [
	  ["Email/import", {"accountId": "a1", "notCreated": {"draft": {"type": "overQuota"}}}, "0"],
	  ["Email/set", {"accountId": "a1"}, "1"]
	], "sessionState": "s1"}`, &calls)
	id, err := (&Client{HTTP: refused.Client()}).SaveDraft(context.Background(), &Session{APIURL: refused.URL}, "a1", "b", "mb", []string{"d1"})
	var draftErr *DraftError
	if id != "" || !errors.As(err, &draftErr) || draftErr.Type != "overQuota" {
		t.Fatalf("refused import = %q, %v", id, err)
	}
	stuck := draftServer(t, `{"methodResponses": [
	  ["Email/import", {"accountId": "a1", "created": {"draft": {"id": "d2"}}}, "0"],
	  ["Email/set", {"accountId": "a1", "notDestroyed": {"d1": {"type": "forbidden"}}}, "1"]
	], "sessionState": "s1"}`, &calls)
	id, err = (&Client{HTTP: stuck.Client()}).SaveDraft(context.Background(), &Session{APIURL: stuck.URL}, "a1", "b", "mb", []string{"d1"})
	if id != "d2" || err == nil || errors.As(err, &draftErr) {
		t.Fatalf("stored but old version kept = %q, %v; want the new id and an error", id, err)
	}
}

// Saves a draft on GOFER_JMAP_TEST_USER's account, saves a second version
// over it, finds it by Message-ID with its revision, and removes it.
func TestSaveDraftLiveServer(t *testing.T) {
	client, session := liveSession(t)
	ctx := context.Background()
	account := session.PrimaryAccount(CapabilityMail)
	drafts := liveMailboxRoles(t, client, session, account)["drafts"]
	if drafts == "" {
		t.Skip("no Drafts mailbox")
	}
	messageID := fmt.Sprintf("<gofer-draft-%d@gofertesting.test>", time.Now().UnixNano())
	save := func(revision string, replace []string) string {
		t.Helper()
		raw := "From: " + os.Getenv("GOFER_JMAP_TEST_USER") + "\r\nSubject: Notes for Friday\r\nMessage-ID: " + messageID + "\r\n" +
			DraftRevisionHeader + ": " + revision + "\r\nDate: " + time.Now().Format(time.RFC1123Z) + "\r\n\r\nVersion " + revision + "\r\n"
		blob, err := client.UploadBlob(ctx, session, account, ContentTypeMessage, []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		id, err := client.SaveDraft(ctx, session, account, blob, drafts, replace)
		if err != nil {
			t.Fatalf("SaveDraft %s: %v", revision, err)
		}
		return id
	}
	first := save("r1", nil)
	second := save("r2", []string{first})
	defer func() {
		_, _ = client.SetEmails(ctx, session, EmailSet{AccountID: account, Destroy: []string{second}})
	}()
	found, err := client.FindDrafts(ctx, session, account, drafts, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != second || found[0].Revision != "r2" || found[0].MessageID != messageID {
		t.Fatalf("found %+v, want only %s with revision r2", found, second)
	}
	got, err := client.GetEmails(ctx, session, account, []string{second}, []string{"mailboxIds", "keywords"})
	if err != nil || len(got.List) != 1 || !got.List[0].MailboxIDs[drafts] || !got.List[0].Keywords[KeywordDraft] {
		t.Fatalf("saved draft = %+v, %v", got, err)
	}
}

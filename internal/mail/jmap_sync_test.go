package mail

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// fakeJMAPServer is a small JMAP server for robin@example.test: Mailbox/get,
// Email/get (by ids or by a reference to a query) and Email/query newest
// first with anchor paging, over whatever mailboxes and emails the test sets.
type fakeJMAPServer struct {
	*httptest.Server
	mu              sync.Mutex
	mailboxes       []jmap.Mailbox
	emails          []jmap.Email
	emailState      string
	maxObjectsInGet int
	// loseAnchorOnce answers the first anchored query with anchorNotFound, as
	// when the anchor email was deleted between two pages.
	loseAnchorOnce bool
	queries        int
}

func newFakeJMAPServer(t *testing.T) *fakeJMAPServer {
	t.Helper()
	f := &fakeJMAPServer{emailState: "es1", maxObjectsInGet: 500}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "robin@example.test" || pass != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/.well-known/jmap":
			f.mu.Lock()
			max := f.maxObjectsInGet
			f.mu.Unlock()
			fmt.Fprintf(w, `{"capabilities": {"urn:ietf:params:jmap:core": {"maxCallsInRequest": 16, "maxObjectsInGet": %d}, "urn:ietf:params:jmap:mail": {}},
				"accounts": {"a1": {"name": "robin@example.test"}}, "primaryAccounts": {"urn:ietf:params:jmap:mail": "a1"},
				"username": "robin@example.test", "apiUrl": "/jmap/", "state": "s1"}`, max)
		case "/jmap/":
			f.answer(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeJMAPServer) answer(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var request struct {
		MethodCalls [][3]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	queryIDs := map[string][]string{}
	var responses []any
	for _, call := range request.MethodCalls {
		var name, callID string
		var args map[string]json.RawMessage
		_ = json.Unmarshal(call[0], &name)
		_ = json.Unmarshal(call[1], &args)
		_ = json.Unmarshal(call[2], &callID)
		switch name {
		case "Mailbox/get":
			responses = append(responses, []any{name, map[string]any{"accountId": "a1", "state": "m1", "list": f.mailboxes, "notFound": []string{}}, callID})
		case "Email/get":
			var ids []string
			if ref, ok := args["#ids"]; ok {
				var reference jmap.ResultReference
				_ = json.Unmarshal(ref, &reference)
				ids = queryIDs[reference.ResultOf]
			} else {
				_ = json.Unmarshal(args["ids"], &ids)
			}
			list := []jmap.Email{}
			for _, id := range ids {
				for _, email := range f.emails {
					if email.ID == id {
						list = append(list, email)
					}
				}
			}
			responses = append(responses, []any{name, map[string]any{"accountId": "a1", "state": f.emailState, "list": list, "notFound": []string{}}, callID})
		case "Email/query":
			f.queries++
			var anchor string
			var anchorOffset, limit int
			_ = json.Unmarshal(args["anchor"], &anchor)
			_ = json.Unmarshal(args["anchorOffset"], &anchorOffset)
			_ = json.Unmarshal(args["limit"], &limit)
			if limit <= 0 || limit > f.maxObjectsInGet {
				limit = f.maxObjectsInGet
			}
			sorted := append([]jmap.Email(nil), f.emails...)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].ReceivedAt.After(sorted[j].ReceivedAt) })
			start := 0
			if anchor != "" {
				found := -1
				for i, email := range sorted {
					if email.ID == anchor {
						found = i
					}
				}
				if found < 0 || f.loseAnchorOnce {
					f.loseAnchorOnce = false
					responses = append(responses, []any{"error", map[string]any{"type": "anchorNotFound"}, callID})
					continue
				}
				start = found + anchorOffset
			}
			ids := []string{}
			for i := start; i < len(sorted) && len(ids) < limit; i++ {
				ids = append(ids, sorted[i].ID)
			}
			queryIDs[callID] = ids
			responses = append(responses, []any{name, map[string]any{"accountId": "a1", "queryState": "q1", "position": start, "ids": ids, "limit": limit}, callID})
		default:
			responses = append(responses, []any{"error", map[string]any{"type": "unknownMethod"}, callID})
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": responses, "sessionState": "s1"})
}

func (f *fakeJMAPServer) setMailboxes(mailboxes ...jmap.Mailbox) {
	f.mu.Lock()
	f.mailboxes = mailboxes
	f.mu.Unlock()
}

func readable() jmap.MailboxRights { return jmap.MailboxRights{MayReadItems: true} }

// useFakeJMAPAccount turns alice's account in the managed fixture into a JMAP
// account on server, with the exceptions its loopback address needs.
func useFakeJMAPAccount(t *testing.T, f *userGmailFixture, server *fakeJMAPServer) {
	t.Helper()
	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	sessionURL := server.URL + "/.well-known/jmap"
	endpoint, err := jmap.ParseEndpoint(sessionURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.system.AddPlaintextTransportException(t.Context(), "http", endpoint.Host, endpoint.Port, "administrator"); err != nil {
		t.Fatal(err)
	}
	if err := f.system.AddPrivateTargetException(t.Context(), "http", endpoint.Host, endpoint.Port, "administrator"); err != nil {
		t.Fatal(err)
	}
	if err := f.worker.accounts.UpdateAccount(t.Context(), "alice", f.ids["alice"], &models.CreateAccountRequest{
		Provider: "jmap", JMAPSessionURL: sessionURL, Username: "robin@example.test", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}
}

type jmapTestFolder struct {
	ID, ParentID, RemoteID, ProviderRemoteID, Name, Role string
	Total, Unread                                        int
	Selectable                                           bool
}

func jmapFolders(t *testing.T, f *userGmailFixture) map[string]jmapTestFolder {
	t.Helper()
	folders := map[string]jmapTestFolder{}
	f.local(t, "alice", func(db *storage.DB) error {
		rows, err := db.Read().Query(`SELECT id, COALESCE(parent_id,''), COALESCE(remote_id,''), provider_remote_id, name, role,
			COALESCE(total_count,0), COALESCE(unread_count,0), COALESCE(selectable,1) FROM folders WHERE account_id=? AND provider_remote_id != ''`, f.ids["alice"])
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var folder jmapTestFolder
			if err := rows.Scan(&folder.ID, &folder.ParentID, &folder.RemoteID, &folder.ProviderRemoteID, &folder.Name, &folder.Role, &folder.Total, &folder.Unread, &folder.Selectable); err != nil {
				return err
			}
			folders[folder.ProviderRemoteID] = folder
		}
		return rows.Err()
	})
	return folders
}

func TestSyncStoresJMAPMailboxesAsFolders(t *testing.T) {
	f := newUserGmailFixture(t)
	server := newFakeJMAPServer(t)
	server.setMailboxes(
		jmap.Mailbox{ID: "mb-in", Name: "Inbox", Role: "inbox", TotalEmails: 3, UnreadEmails: 1, MyRights: readable()},
		jmap.Mailbox{ID: "mb-sent", Name: "Sent Items", Role: "sent", TotalEmails: 2, MyRights: readable()},
		jmap.Mailbox{ID: "mb-proj", Name: "Projects", MyRights: readable()},
		jmap.Mailbox{ID: "mb-2026", Name: "2026", ParentID: "mb-proj", TotalEmails: 4, UnreadEmails: 4, MyRights: readable()},
	)
	useFakeJMAPAccount(t, f, server)

	if err := f.worker.Sync(t.Context(), "alice", f.ids["alice"]); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	folders := jmapFolders(t, f)
	inbox, sent, projects, year := folders["mb-in"], folders["mb-sent"], folders["mb-proj"], folders["mb-2026"]
	if inbox.Role != "inbox" || inbox.Name != "Inbox" || inbox.Total != 3 || inbox.Unread != 1 || !inbox.Selectable {
		t.Fatalf("inbox = %+v", inbox)
	}
	// The server's name for a special mailbox gives way to Gofer's.
	if sent.Role != "sent" || sent.Name != "Sent" {
		t.Fatalf("sent = %+v", sent)
	}
	if projects.Role != "custom" || projects.Name != "Projects" || projects.ParentID != "" {
		t.Fatalf("projects = %+v", projects)
	}
	if year.ParentID != projects.ID || year.RemoteID != "Projects/2026" || year.Unread != 4 {
		t.Fatalf("child = %+v (parent %q)", year, projects.ID)
	}

	// A renamed mailbox keeps its folder.
	server.setMailboxes(
		jmap.Mailbox{ID: "mb-in", Name: "Inbox", Role: "inbox", TotalEmails: 3, MyRights: readable()},
		jmap.Mailbox{ID: "mb-sent", Name: "Sent Items", Role: "sent", TotalEmails: 2, MyRights: readable()},
		jmap.Mailbox{ID: "mb-proj", Name: "Work", MyRights: readable()},
		jmap.Mailbox{ID: "mb-2026", Name: "2026", ParentID: "mb-proj", MyRights: readable()},
	)
	if err := f.worker.Sync(t.Context(), "alice", f.ids["alice"]); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	folders = jmapFolders(t, f)
	if renamed := folders["mb-proj"]; renamed.ID != projects.ID || renamed.Name != "Work" {
		t.Fatalf("renamed folder = %+v, was %+v", renamed, projects)
	}
	if child := folders["mb-2026"]; child.RemoteID != "Work/2026" || child.ParentID != projects.ID {
		t.Fatalf("child after rename = %+v", child)
	}
	if folders["mb-in"].Unread != 0 {
		t.Fatalf("inbox unread not updated: %+v", folders["mb-in"])
	}
}

type jmapTestMessage struct {
	Folders         map[string]bool
	Read, Flagged   bool
	Subject, From   string
	InternetMessage string
}

// jmapMessages reads alice's stored JMAP messages by provider id, with the
// mailboxes (by provider id) each shows in.
func jmapMessages(t *testing.T, f *userGmailFixture) map[string]*jmapTestMessage {
	t.Helper()
	messages := map[string]*jmapTestMessage{}
	f.local(t, "alice", func(db *storage.DB) error {
		rows, err := db.Read().Query(`SELECT m.remote_message_id, f.provider_remote_id, mfs.is_read, mfs.is_flagged, m.subject, m.from_email, m.internet_message_id
			FROM messages m JOIN message_folder_state mfs ON mfs.message_id = m.id AND mfs.is_deleted = 0
			JOIN folders f ON f.id = mfs.folder_id WHERE m.account_id = ?`, f.ids["alice"])
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, mailbox, subject, from, internetID string
			var read, flagged bool
			if err := rows.Scan(&id, &mailbox, &read, &flagged, &subject, &from, &internetID); err != nil {
				return err
			}
			message := messages[id]
			if message == nil {
				message = &jmapTestMessage{Folders: map[string]bool{}, Read: read, Flagged: flagged, Subject: subject, From: from, InternetMessage: internetID}
				messages[id] = message
			}
			message.Folders[mailbox] = true
		}
		return rows.Err()
	})
	return messages
}

func jmapCursor(t *testing.T, f *userGmailFixture, scope string) string {
	t.Helper()
	var cursor string
	f.local(t, "alice", func(db *storage.DB) error {
		state, err := db.GetLabelSyncState(t.Context(), f.ids["alice"], storage.LabelProviderJMAP, scope)
		cursor = state.Cursor
		return err
	})
	return cursor
}

func testJMAPEmail(id, subject string, minutesAgo int, mailboxes ...string) jmap.Email {
	email := jmap.Email{
		ID: id, BlobID: "blob-" + id, ThreadID: "t-" + id, Subject: subject, Size: 1200,
		MailboxIDs: map[string]bool{}, Keywords: map[string]bool{},
		From:       []jmap.EmailAddress{{Name: "Sam Ortiz", Email: "sam@example.test"}},
		To:         []jmap.EmailAddress{{Email: "robin@example.test"}},
		MessageID:  []string{id + "@example.test"},
		ReceivedAt: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC).Add(-time.Duration(minutesAgo) * time.Minute),
		Preview:    "About " + subject,
	}
	for _, mailbox := range mailboxes {
		email.MailboxIDs[mailbox] = true
	}
	return email
}

// The first sync imports every email page by page, one row per mailbox it is
// in, and only then saves the states read before the import as cursors.
func TestSyncImportsJMAPEmails(t *testing.T) {
	f := newUserGmailFixture(t)
	server := newFakeJMAPServer(t)
	server.maxObjectsInGet = 2
	server.loseAnchorOnce = true
	server.emailState = "es-before"
	server.setMailboxes(
		jmap.Mailbox{ID: "mb-in", Name: "Inbox", Role: "inbox", MyRights: readable()},
		jmap.Mailbox{ID: "mb-arch", Name: "Archive", Role: "archive", MyRights: readable()},
		jmap.Mailbox{ID: "mb-proj", Name: "Projects", MyRights: readable()},
	)
	unread := testJMAPEmail("e1", "Lunch on Friday?", 1, "mb-in")
	labelled := testJMAPEmail("e2", "Garden plans", 2, "mb-in", "mb-proj")
	labelled.Keywords = map[string]bool{"$seen": true, "$flagged": true}
	archived := testJMAPEmail("e3", "Old receipt", 3, "mb-arch")
	archived.Keywords = map[string]bool{"$seen": true}
	draft := testJMAPEmail("e4", "Notes", 4, "mb-in")
	elsewhere := testJMAPEmail("e5", "Somewhere unknown", 5, "mb-hidden")
	server.emails = []jmap.Email{unread, labelled, archived, draft, elsewhere}
	useFakeJMAPAccount(t, f, server)

	if err := f.worker.Sync(t.Context(), "alice", f.ids["alice"]); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	messages := jmapMessages(t, f)
	if len(messages) != 4 || messages["e5"] != nil {
		t.Fatalf("stored %d messages (%v), want e1-e4", len(messages), messages)
	}
	if m := messages["e1"]; m.Read || m.Flagged || !m.Folders["mb-in"] || m.Subject != "Lunch on Friday?" || m.From != "sam@example.test" || m.InternetMessage != "<e1@example.test>" {
		t.Fatalf("e1 = %+v", m)
	}
	if m := messages["e2"]; !m.Read || !m.Flagged || !m.Folders["mb-in"] || !m.Folders["mb-proj"] || len(m.Folders) != 2 {
		t.Fatalf("e2 = %+v", m)
	}
	if m := messages["e3"]; !m.Folders["mb-arch"] || len(m.Folders) != 1 {
		t.Fatalf("e3 = %+v", m)
	}
	// Five emails two at a time is three pages; the lost anchor restarts once.
	if server.queries < 4 {
		t.Fatalf("server answered %d queries, want paging with one restart", server.queries)
	}
	if got := jmapCursor(t, f, jmapEmailScope); got != "es-before" {
		t.Fatalf("email cursor = %q, want the state read before importing", got)
	}
	if got := jmapCursor(t, f, jmapMailboxScope); got != "m1" {
		t.Fatalf("mailbox cursor = %q", got)
	}

	// With a cursor saved, the next sync does not import again.
	queries := server.queries
	if err := f.worker.Sync(t.Context(), "alice", f.ids["alice"]); err != nil {
		t.Fatal(err)
	}
	if server.queries != queries {
		t.Fatal("imported again although a cursor was saved")
	}
}

package mail

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// fakeJMAPServer serves a session for robin@example.test and answers
// Mailbox/get with whatever mailboxes the test sets.
type fakeJMAPServer struct {
	*httptest.Server
	mu        sync.Mutex
	mailboxes []jmap.Mailbox
}

func newFakeJMAPServer(t *testing.T) *fakeJMAPServer {
	t.Helper()
	f := &fakeJMAPServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "robin@example.test" || pass != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/.well-known/jmap":
			_, _ = io.WriteString(w, `{"capabilities": {"urn:ietf:params:jmap:core": {"maxCallsInRequest": 16}, "urn:ietf:params:jmap:mail": {}},
				"accounts": {"a1": {"name": "robin@example.test"}}, "primaryAccounts": {"urn:ietf:params:jmap:mail": "a1"},
				"username": "robin@example.test", "apiUrl": "/jmap/", "state": "s1"}`)
		case "/jmap/":
			var request struct {
				MethodCalls [][]json.RawMessage `json:"methodCalls"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			var name string
			_ = json.Unmarshal(request.MethodCalls[0][0], &name)
			if name != "Mailbox/get" {
				http.Error(w, "unexpected "+name, http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			list, _ := json.Marshal(f.mailboxes)
			f.mu.Unlock()
			_, _ = w.Write([]byte(`{"methodResponses": [["Mailbox/get", {"accountId": "a1", "state": "m1", "notFound": [], "list": ` + string(list) + `}, "0"]], "sessionState": "s1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
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

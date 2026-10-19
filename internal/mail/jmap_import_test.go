package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
)

// jmapImportServer is a JMAP server that keeps the mailboxes it is asked to
// create and the emails it is asked to import.
type jmapImportServer struct {
	mu        sync.Mutex
	mailboxes []jmap.Mailbox
	imported  []map[string]any
	sessions  int
	failNext  bool
}

func newJMAPImportServer(t *testing.T, maxUpload int, mailboxes ...jmap.Mailbox) (*jmapImportServer, *httptest.Server) {
	t.Helper()
	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	f := &jmapImportServer{mailboxes: mailboxes}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/.well-known/jmap":
			f.sessions++
			_, _ = fmt.Fprintf(w, `{"capabilities": {%q: {"maxSizeUpload": %d}, %q: {}},
				"accounts": {"a1": {"name": "robin@example.test", "isPersonal": true}},
				"primaryAccounts": {%q: "a1"}, "username": "robin@example.test",
				"apiUrl": %q, "uploadUrl": %q, "state": "s1"}`,
				jmap.CapabilityCore, maxUpload, jmap.CapabilityMail, jmap.CapabilityMail,
				server.URL+"/jmap/", server.URL+"/upload/{accountId}/")
		case "/upload/a1/":
			body, _ := io.ReadAll(r.Body)
			_, _ = fmt.Fprintf(w, `{"accountId": "a1", "blobId": "blob-%d", "type": "message/rfc822", "size": %d}`, len(f.imported), len(body))
		case "/jmap/":
			if f.failNext {
				f.failNext = false
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			f.answer(t, w, decodeJMAPCalls(t, r))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return f, server
}

func decodeJMAPCalls(t *testing.T, r *http.Request) [][]json.RawMessage {
	t.Helper()
	var request struct {
		MethodCalls [][]json.RawMessage `json:"methodCalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Fatalf("request is not JSON: %v", err)
	}
	return request.MethodCalls
}

func (f *jmapImportServer) answer(t *testing.T, w http.ResponseWriter, calls [][]json.RawMessage) {
	var name string
	_ = json.Unmarshal(calls[0][0], &name)
	var response any
	switch name {
	case "Mailbox/get":
		response = map[string]any{"accountId": "a1", "state": "s1", "list": f.mailboxes}
	case "Mailbox/set":
		var args struct {
			Create map[string]struct {
				Name     string  `json:"name"`
				ParentID *string `json:"parentId"`
			} `json:"create"`
		}
		_ = json.Unmarshal(calls[0][1], &args)
		created := map[string]any{}
		for key, mailbox := range args.Create {
			id := fmt.Sprintf("mb-new-%d", len(f.mailboxes))
			parent := ""
			if mailbox.ParentID != nil {
				parent = *mailbox.ParentID
			}
			f.mailboxes = append(f.mailboxes, jmap.Mailbox{ID: id, Name: mailbox.Name, ParentID: parent})
			created[key] = map[string]string{"id": id}
		}
		response = map[string]any{"accountId": "a1", "created": created}
	case "Email/import":
		var args struct {
			Emails map[string]map[string]any `json:"emails"`
		}
		_ = json.Unmarshal(calls[0][1], &args)
		created := map[string]any{}
		for key, email := range args.Emails {
			f.imported = append(f.imported, email)
			created[key] = map[string]string{"id": fmt.Sprintf("e%d", len(f.imported))}
		}
		response = map[string]any{"accountId": "a1", "created": created}
	default:
		t.Errorf("unexpected call %s", name)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{name, response, "0"}}, "sessionState": "s1"})
}

// A path finds the mailboxes it names, including the inbox by its role, and
// creates the ones missing, each inside the one before.
func TestJMAPImportEnsuresMailboxPaths(t *testing.T) {
	f, server := newJMAPImportServer(t, 0,
		jmap.Mailbox{ID: "mb-inbox", Name: "Inbox", Role: "inbox"},
		jmap.Mailbox{ID: "mb-work", Name: "Work"},
	)
	imports := NewJMAPImport(jmapTestConfig(server.URL+"/.well-known/jmap", true, true), "secret")
	ctx := context.Background()
	for path, want := range map[string]string{"INBOX": "mb-inbox", "Work": "mb-work"} {
		if id, err := imports.EnsureMailbox(ctx, path); err != nil || id != want {
			t.Fatalf("EnsureMailbox(%q) = %q, %v", path, id, err)
		}
	}
	id, err := imports.EnsureMailbox(ctx, "Work/2024/Receipts")
	if err != nil {
		t.Fatal(err)
	}
	again, err := imports.EnsureMailbox(ctx, "Work/2024/Receipts")
	if err != nil || again != id {
		t.Fatalf("second EnsureMailbox = %q, %v; first %q", again, err, id)
	}
	want := []jmap.Mailbox{
		{ID: "mb-inbox", Name: "Inbox", Role: "inbox"},
		{ID: "mb-work", Name: "Work"},
		{ID: "mb-new-2", Name: "2024", ParentID: "mb-work"},
		{ID: "mb-new-3", Name: "Receipts", ParentID: "mb-new-2"},
	}
	if !reflect.DeepEqual(f.mailboxes, want) || id != "mb-new-3" {
		t.Fatalf("mailboxes = %+v, id %q", f.mailboxes, id)
	}
	if f.sessions != 1 {
		t.Fatalf("signed in %d times", f.sessions)
	}
}

// Import keeps the keywords and date, signs in again after a failed request,
// and refuses a message over the upload limit as final.
func TestJMAPImportStoresMessages(t *testing.T) {
	f, server := newJMAPImportServer(t, 64)
	imports := NewJMAPImport(jmapTestConfig(server.URL+"/.well-known/jmap", true, true), "secret")
	ctx := context.Background()
	raw := []byte("Subject: hi\r\n\r\nhello\r\n")
	date := time.Date(2023, 7, 1, 8, 0, 0, 0, time.UTC)
	if err := imports.Import(ctx, "mb-inbox", raw, []string{jmap.KeywordSeen}, date); err != nil {
		t.Fatal(err)
	}
	f.failNext = true
	if err := imports.Import(ctx, "mb-inbox", raw, nil, time.Time{}); err == nil {
		t.Fatal("a failed request was not reported")
	}
	if err := imports.Import(ctx, "mb-inbox", raw, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if f.sessions != 2 {
		t.Fatalf("signed in %d times, want again after the failure", f.sessions)
	}
	first := f.imported[0]
	if first["blobId"] != "blob-0" || !reflect.DeepEqual(first["mailboxIds"], map[string]any{"mb-inbox": true}) ||
		!reflect.DeepEqual(first["keywords"], map[string]any{"$seen": true}) || first["receivedAt"] != "2023-07-01T08:00:00Z" {
		t.Fatalf("import = %v", first)
	}
	err := imports.Import(ctx, "mb-inbox", make([]byte, 65), nil, time.Time{})
	var refused *jmap.ImportError
	if !errors.As(err, &refused) || refused.Type != "tooLarge" || len(f.imported) != 2 {
		t.Fatalf("too large = %v after %d imports", err, len(f.imported))
	}
}

package mail

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/config"
	"github.com/cristianadrielbraun/gofer/internal/mail/imap"
	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
)

// pushTestServer serves a JMAP session for account a1 and hands each event
// source connection to stream. withPush false leaves eventSourceUrl out.
func pushTestServer(t *testing.T, withPush bool, stream func(n int, w io.Writer, flush func(), done <-chan struct{})) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	connections := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/jmap":
			events := ""
			if withPush {
				events = `"eventSourceUrl": "/events?types={types}&closeafter={closeafter}&ping={ping}",`
			}
			_, _ = io.WriteString(w, `{"capabilities": {"urn:ietf:params:jmap:core": {}, "urn:ietf:params:jmap:mail": {}},
				"accounts": {"a1": {"name": "robin@example.test"}}, "primaryAccounts": {"urn:ietf:params:jmap:mail": "a1"},
				`+events+` "apiUrl": "/jmap/", "state": "s1"}`)
		case "/events":
			mu.Lock()
			connections++
			n := connections
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			flusher.Flush()
			stream(n, w, flusher.Flush, r.Context().Done())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func pushTestConfig(server *httptest.Server) *models.AccountConfig {
	return &models.AccountConfig{AccountID: "acc-push", Provider: "jmap", JMAPSessionURL: server.URL + "/.well-known/jmap",
		Username: "robin@example.test", JMAPAllowPlaintext: true, JMAPAllowPrivate: true}
}

// pushRecorder collects a watcher's notifications and statuses.
type pushRecorder struct {
	mu       sync.Mutex
	notified int
	statuses []imap.IdleWatcherStatus
	changed  chan struct{}
}

func newPushRecorder() *pushRecorder { return &pushRecorder{changed: make(chan struct{}, 64)} }

func (r *pushRecorder) notify() {
	r.mu.Lock()
	r.notified++
	r.mu.Unlock()
	r.changed <- struct{}{}
}

func (r *pushRecorder) status(status imap.IdleWatcherStatus) {
	r.mu.Lock()
	r.statuses = append(r.statuses, status)
	r.mu.Unlock()
	r.changed <- struct{}{}
}

// waitFor waits until ok holds for the recorder, up to five seconds.
func (r *pushRecorder) waitFor(t *testing.T, what string, ok func(*pushRecorder) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		r.mu.Lock()
		done := ok(r)
		r.mu.Unlock()
		if done {
			return
		}
		select {
		case <-r.changed:
		case <-deadline:
			t.Fatalf("timed out waiting for %s: notified %d, statuses %+v", what, r.notified, r.statuses)
		}
	}
}

// A push for the account's mail or mailboxes triggers a sync, one for another
// account does not, and a dropped connection is reported and reopened, with
// a sync for what changed while it was down.
func TestJMAPPushWatcherNotifiesAndReconnects(t *testing.T) {
	server := pushTestServer(t, true, func(n int, w io.Writer, flush func(), done <-chan struct{}) {
		if n == 1 {
			_, _ = io.WriteString(w, "event: state\ndata: {\"@type\":\"StateChange\",\"changed\":{\"a2\":{\"Email\":\"x\"}}}\n\n")
			_, _ = io.WriteString(w, "event: state\ndata: {\"@type\":\"StateChange\",\"changed\":{\"a1\":{\"Mailbox\":\"m2\"}}}\n\n")
			flush()
			return // the server drops the connection
		}
		_, _ = io.WriteString(w, "event: state\ndata: {\"@type\":\"StateChange\",\"changed\":{\"a1\":{\"Email\":\"s9\"}}}\n\n")
		flush()
		<-done
	})
	record := newPushRecorder()
	watcher := NewJMAPPushWatcher(pushTestConfig(server), "secret", record.notify, record.status)
	finished := make(chan struct{})
	go func() { watcher.Run(t.Context()); close(finished) }()

	// The Mailbox push, the reconnection and the Email push.
	record.waitFor(t, "three notifications", func(r *pushRecorder) bool { return r.notified == 3 })
	watcher.Close()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Close")
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.notified != 3 || len(record.statuses) < 3 {
		t.Fatalf("notified %d, statuses %+v", record.notified, record.statuses)
	}
	connected, lost, reconnected := record.statuses[0], record.statuses[1], record.statuses[2]
	if !connected.Healthy || lost.Healthy || lost.ReasonCode != "connection_lost" || lost.RetryAt.IsZero() || !reconnected.Healthy {
		t.Fatalf("statuses = %+v, want connected, lost, connected", record.statuses)
	}
}

// A server that accepts the push connection and closes it at once is
// retried with a growing wait, not every second; a connection that stays
// open long enough starts the wait over.
func TestJMAPPushWatcherBacksOffFromConnectionsThatCloseAtOnce(t *testing.T) {
	initial, stable := jmapPushInitialBackoff, jmapPushStableAfter
	jmapPushInitialBackoff, jmapPushStableAfter = 20*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { jmapPushInitialBackoff, jmapPushStableAfter = initial, stable })
	var mu sync.Mutex
	var waits []time.Duration
	server := pushTestServer(t, true, func(n int, w io.Writer, flush func(), done <-chan struct{}) {
		if n == 5 {
			// This one stays open long enough to count as working.
			select {
			case <-time.After(2 * jmapPushStableAfter):
			case <-done:
			}
		}
	})
	enough := make(chan struct{})
	watcher := NewJMAPPushWatcher(pushTestConfig(server), "secret", nil, func(status imap.IdleWatcherStatus) {
		if status.Healthy {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, time.Until(status.RetryAt).Round(10*time.Millisecond))
		if len(waits) == 6 {
			close(enough)
		}
	})
	finished := make(chan struct{})
	go func() { watcher.Run(t.Context()); close(finished) }()
	select {
	case <-enough:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher stopped reconnecting")
	}
	watcher.Close()
	<-finished
	mu.Lock()
	defer mu.Unlock()
	// 20, 40, 80 and 160 ms after the connections that closed at once; the
	// fifth stayed open, so the wait after it starts over.
	ms := time.Millisecond
	want := []time.Duration{20 * ms, 40 * ms, 80 * ms, 160 * ms, 20 * ms, 40 * ms}
	if !slices.Equal(waits, want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
}

// A server without push is reported as such and not retried soon; periodic
// sync keeps covering the account.
func TestJMAPPushWatcherReportsMissingPush(t *testing.T) {
	server := pushTestServer(t, false, nil)
	record := newPushRecorder()
	watcher := NewJMAPPushWatcher(pushTestConfig(server), "secret", record.notify, record.status)
	finished := make(chan struct{})
	go func() { watcher.Run(t.Context()); close(finished) }()
	record.waitFor(t, "a status", func(r *pushRecorder) bool { return len(r.statuses) > 0 })
	watcher.Close()
	<-finished
	status := record.statuses[0]
	if status.Healthy || status.ReasonCode != "push_unsupported" || time.Until(status.RetryAt) < 25*time.Minute {
		t.Fatalf("status = %+v, want push_unsupported retried in about 30 minutes", status)
	}
}

// In managed mode an owner with Gofer open has their JMAP account watched
// over one push connection: a push brings new mail in without waiting for
// the poll, and the connection ends once they close Gofer.
func TestUserIMAPWatchesJMAPAccountsOverPush(t *testing.T) {
	f := newUserGmailFixture(t)
	server := newFakeJMAPServer(t)
	server.pushes = make(chan string, 4)
	server.emailState = "c0"
	server.setMailboxes(
		jmap.Mailbox{ID: "mb-in", Name: "Inbox", Role: "inbox", MyRights: readable()},
		jmap.Mailbox{ID: "mb-arch", Name: "Archive", Role: "archive", MyRights: readable()},
	)
	server.emails = []jmap.Email{testJMAPEmail("e1", "Lunch on Friday?", 2, "mb-in")}
	useFakeJMAPAccount(t, f, server)
	if err := f.worker.Start(UserIMAPBackgroundOptions{PollInterval: time.Hour, IdleOfflineGrace: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	streams := func() int { server.mu.Lock(); defer server.mu.Unlock(); return server.streams }
	release, err := f.worker.BeginActiveUserSession(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitJMAP(t, "one push connection after the first sync", func() bool { return jmapMessages(t, f)["e1"] != nil && streams() == 1 })

	server.addEmail(testJMAPEmail("e2", "Garden plans", 0, "mb-in"))
	server.pushes <- `{"@type":"StateChange","changed":{"a1":{"Email":"c1"}}}`
	waitJMAP(t, "the pushed email", func() bool { return jmapMessages(t, f)["e2"] != nil })
	if streams() != 1 {
		t.Fatalf("%d push connections, want one for the whole account", streams())
	}

	release()
	waitJMAP(t, "the push connection to end", func() bool { return streams() == 0 })
}

func waitJMAP(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// In shared mode a JMAP account is watched over push once its first sync is
// done. A push that comes while the account is already syncing is not lost:
// the account syncs again once that sync ends.
func TestSyncOrchestratorWatchesJMAPAccountsOverPush(t *testing.T) {
	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	ctx := t.Context()
	server := newFakeJMAPServer(t)
	server.pushes = make(chan string, 4)
	server.emailState = "c0"
	server.setMailboxes(jmap.Mailbox{ID: "mb-in", Name: "Inbox", Role: "inbox", MyRights: readable()})
	server.emails = []jmap.Email{testJMAPEmail("e1", "Lunch on Friday?", 2, "mb-in")}
	db := newLabelSyncTestDB(t)
	endpoint, err := jmap.ParseEndpoint(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddPlaintextTransportException(ctx, "http", endpoint.Host, endpoint.Port, "default"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddPrivateTargetException(ctx, "http", endpoint.Host, endpoint.Port, "default"); err != nil {
		t.Fatal(err)
	}
	accounts, err := config.NewAccountStore(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.CreateAccount(ctx, "default", &models.CreateAccountRequest{
		Provider: "jmap", EmailAddress: "robin@example.test", JMAPSessionURL: server.URL + "/.well-known/jmap",
		Username: "robin@example.test", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	orchestrator := NewSyncOrchestrator(db, accounts, nil, nil)
	orchestrator.StartAccount(ctx, account.ID)
	t.Cleanup(func() { orchestrator.StopAccount(account.ID) })
	stored := func(emailID string) bool {
		var n int
		_ = db.Read().QueryRow(`SELECT COUNT(*) FROM messages WHERE account_id=? AND remote_message_id=?`, account.ID, emailID).Scan(&n)
		return n > 0
	}
	streams := func() int { server.mu.Lock(); defer server.mu.Unlock(); return server.streams }
	waitJMAP(t, "the first sync and a push connection", func() bool { return stored("e1") && streams() == 1 })

	server.addEmail(testJMAPEmail("e2", "Garden plans", 1, "mb-in"))
	server.pushes <- `{"@type":"StateChange","changed":{"a1":{"Email":"c1"}}}`
	waitJMAP(t, "the pushed email", func() bool { return stored("e2") })

	// Another sync is running when the next push comes in. The push's own
	// sync may still be finishing, so take the next turn.
	var finish func()
	waitJMAP(t, "a turn to sync", func() bool {
		var ok bool
		_, finish, ok = orchestrator.beginAccountSync(ctx, account.ID, accountSyncManual)
		return ok
	})
	server.addEmail(testJMAPEmail("e3", "New invoice", 0, "mb-in"))
	server.pushes <- `{"@type":"StateChange","changed":{"a1":{"Email":"c2"}}}`
	time.Sleep(200 * time.Millisecond)
	if stored("e3") {
		t.Fatal("synced while another sync was running")
	}
	finish()
	waitJMAP(t, "the email pushed during the other sync", func() bool { return stored("e3") })
}

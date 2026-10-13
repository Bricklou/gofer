package mail

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newCapacityTestIMAP(t *testing.T, queueLimit int) *UserIMAP {
	t.Helper()
	return &UserIMAP{
		ctx:         t.Context(),
		queue:       make(chan userIMAPJob, syncQueueSize),
		queueOnline: make(chan userIMAPJob, syncQueueSize),
		onlineUsers: make(map[string]int),
		queued:      make(map[string]*userIMAPQueueState),
		queueLimit:  queueLimit,
		serverSlots: make(map[string]*serverSlot),
	}
}

func TestOnlineOwnersAccountsAreSyncedFirst(t *testing.T) {
	s := newCapacityTestIMAP(t, 10)
	s.mu.Lock()
	for _, job := range []userIMAPJob{{"bob", "bob-1"}, {"bob", "bob-2"}} {
		if err := s.enqueueLocked(job, false); err != nil {
			t.Fatal(err)
		}
	}
	s.onlineUsers["alice"] = 1
	if err := s.enqueueLocked(userIMAPJob{"alice", "alice-1"}, false); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	var order []string
	for range 3 {
		job, ok := s.nextJob()
		if !ok {
			t.Fatal("no job")
		}
		order = append(order, job.account)
	}
	if order[0] != "alice-1" || order[1] != "bob-1" || order[2] != "bob-2" {
		t.Fatalf("sync order %v, want the online owner's account first", order)
	}
}

func TestSyncQueueLimitReportsFull(t *testing.T) {
	s := newCapacityTestIMAP(t, 2)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range []string{"a", "b"} {
		if err := s.enqueueLocked(userIMAPJob{"owner", id}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.enqueueLocked(userIMAPJob{"owner", "c"}, false); !errors.Is(err, errUserIMAPQueueFull) {
		t.Fatalf("third account: %v, want a full queue", err)
	}
	// An account already waiting is not queued twice and is not refused.
	if err := s.enqueueLocked(userIMAPJob{"owner", "a"}, true); err != nil {
		t.Fatalf("already queued account refused: %v", err)
	}
}

func TestBackgroundSyncsPerServerAreLimited(t *testing.T) {
	s := newCapacityTestIMAP(t, 10)
	s.maxPerServer = 2
	ctx := context.Background()
	first, err := s.acquireServer(ctx, "IMAP.Example.test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.acquireServer(ctx, "imap.example.test")
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := s.acquireServer(short, "imap.example.test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third sync to a full server: %v, want it to wait", err)
	}
	other, err := s.acquireServer(ctx, "mail.other.test")
	if err != nil {
		t.Fatalf("another server held back: %v", err)
	}
	other()
	first()
	third, err := s.acquireServer(ctx, "imap.example.test")
	if err != nil {
		t.Fatalf("released slot not reused: %v", err)
	}
	second()
	third()
	if len(s.serverSlots) != 0 {
		t.Fatalf("server slots kept after release: %v", s.serverSlots)
	}
}

func TestOpenBrowserMarksOwnerOnline(t *testing.T) {
	for _, poll := range []string{"off", ""} {
		t.Run("gmail poll "+poll, func(t *testing.T) {
			t.Setenv("GOFER_GMAIL_API_POLL", poll)
			f := newUserGmailFixture(t)
			t.Cleanup(func() { f.cancel(); f.worker.Wait() })
			first, err := f.worker.BeginActiveUserSession(t.Context(), "alice")
			if err != nil {
				t.Fatal(err)
			}
			second, err := f.worker.BeginActiveUserSession(t.Context(), "alice")
			if err != nil {
				t.Fatal(err)
			}
			online := func() bool {
				f.worker.mu.Lock()
				defer f.worker.mu.Unlock()
				return f.worker.queueFor("alice") == f.worker.queueOnline
			}
			if !online() {
				t.Fatal("owner with Gofer open not served first")
			}
			first()
			first() // Releasing twice counts once.
			if !online() {
				t.Fatal("owner offline while another tab is still open")
			}
			second()
			if online() {
				t.Fatal("owner still served first after closing every tab")
			}
		})
	}
}

func TestStartSetsConfiguredSyncWorkers(t *testing.T) {
	f := newUserGmailFixture(t)
	t.Cleanup(func() { f.cancel(); f.worker.Wait() })
	if err := f.worker.Start(UserIMAPBackgroundOptions{PollInterval: time.Hour, DisableIDLE: true, MaxConcurrentSyncs: 10}); err != nil {
		t.Fatal(err)
	}
	f.worker.mu.Lock()
	count, limit := f.worker.workerCount, f.worker.workerLimit
	f.worker.mu.Unlock()
	if count != 10 || limit != 10 {
		t.Fatalf("workers %d, limit %d, want 10", count, limit)
	}
	if err := f.worker.Start(UserIMAPBackgroundOptions{MaxConcurrentSyncs: maxSyncWorkers + 1}); err == nil {
		t.Fatal("second start or too many workers accepted")
	}
}

// When a watch limit is reached, an Inbox takes the place of another folder's
// watch; other folders never displace anything.
func TestIdleLimitsPreferInboxes(t *testing.T) {
	s := newCapacityTestIMAP(t, 10)
	s.watches = make(map[userIMAPWatchKey]*userIMAPWatch)
	s.idleByHost = make(map[string]int)
	s.backgroundOptions.MaxIdleWatchers = 2
	s.maxIdlePerServer = 10
	cancelled := map[string]bool{}
	add := func(account, role, host string) {
		s.watches[userIMAPWatchKey{account, account + "-" + role}] = &userIMAPWatch{owner: account, role: role, host: host, cancel: func() { cancelled[account+"-"+role] = true }}
		s.idleCount++
		s.idleByHost[host]++
	}
	add("bob", "sent", "mail.example.test")
	add("carol", "inbox", "mail.example.test")

	if room, freeing := s.idleRoomLocked("mail.example.test", "sent"); room || freeing {
		t.Fatal("a Sent folder displaced another watch")
	}
	room, freeing := s.idleRoomLocked("mail.example.test", "inbox")
	if room || !freeing || !cancelled["bob-sent"] || cancelled["carol-inbox"] {
		t.Fatalf("inbox at the limit: room=%v freeing=%v cancelled=%v; want Bob's Sent watch stopped", room, freeing, cancelled)
	}
	// With only Inboxes left there is nothing to displace.
	delete(cancelled, "bob-sent")
	s.watches = map[userIMAPWatchKey]*userIMAPWatch{}
	add("carol", "inbox", "mail.example.test")
	add("dave", "inbox", "mail.example.test")
	s.idleCount = 2
	if room, freeing := s.idleRoomLocked("mail.example.test", "inbox"); room || freeing || len(cancelled) != 0 {
		t.Fatal("an Inbox displaced another Inbox")
	}

	// A full server only displaces watches on that server.
	s.watches = map[userIMAPWatchKey]*userIMAPWatch{}
	s.idleByHost = map[string]int{}
	s.idleCount = 0
	s.backgroundOptions.MaxIdleWatchers = 100
	s.maxIdlePerServer = 1
	add("erin", "sent", "other.example.test")
	add("frank", "sent", "mail.example.test")
	room, freeing = s.idleRoomLocked("mail.example.test", "inbox")
	if room || !freeing || !cancelled["frank-sent"] || cancelled["erin-sent"] {
		t.Fatalf("full server: cancelled %v, want only the watch on that server", cancelled)
	}
}

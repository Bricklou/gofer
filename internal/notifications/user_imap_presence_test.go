package notifications

import (
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/mail"
)

// Closing one tab does not end watching while another is still open.
func TestUserIMAPKeepsWatchingWhileAnotherTabIsOpen(t *testing.T) {
	f := newUserStorageFixtureMode(t, true)
	s := newRoutedIMAPServer(t)
	s.idleSupported = true
	// startBackgroundIMAP opens a session for each owner.
	startBackgroundIMAP(t, f, s, mail.UserIMAPBackgroundOptions{PollInterval: time.Hour, IdleOfflineGrace: 100 * time.Millisecond})
	waitUserIMAP(t, func() bool { return len(s.idlePeers("alice")) == 1 && len(s.idlePeers("bob")) == 1 })
	release, err := f.imap.BeginActiveUserSession(t.Context(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	release()
	time.Sleep(300 * time.Millisecond)
	if len(s.idlePeers("bob")) != 1 {
		t.Fatal("Bob's watch ended while another tab is still open")
	}
}

func TestUserIMAPOnlineStartsAndOfflineEndsWatching(t *testing.T) {
	f := newUserStorageFixtureMode(t, true)
	s := newRoutedIMAPServer(t)
	s.idleSupported = true
	f.useIMAPServer(t, s)
	if err := f.imap.Start(mail.UserIMAPBackgroundOptions{PollInterval: time.Hour, IdleOfflineGrace: 100 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	waitUserIMAP(t, func() bool { return f.messageCount(t, "alice") == 2 && f.messageCount(t, "bob") == 2 })
	if n := len(s.idlePeers("alice")) + len(s.idlePeers("bob")); n != 0 {
		t.Fatalf("%d folders watched with nobody online", n)
	}

	// Mail that arrived while away appears as soon as Alice opens Gofer, and
	// her Inbox is watched from then on.
	s.mu.Lock()
	s.ownerUIDs["alice"] = []uint32{2, 1000000, 1000001}
	s.mu.Unlock()
	release, err := f.imap.BeginActiveUserSession(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	waitUserIMAP(t, func() bool { return f.messageCount(t, "alice") == 3 && len(s.idlePeers("alice")) == 1 })
	if len(s.idlePeers("bob")) != 0 {
		t.Fatal("Bob watched although he is away")
	}
	s.mu.Lock()
	s.ownerUIDs["alice"] = []uint32{2, 1000000, 1000001, 1000002}
	s.mu.Unlock()
	s.notifyIdle("alice", "* 4 EXISTS")
	waitUserIMAP(t, func() bool { return f.messageCount(t, "alice") == 4 })

	// A reload within the grace period keeps the watch.
	release()
	again, err := f.imap.BeginActiveUserSession(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if len(s.idlePeers("alice")) != 1 {
		t.Fatal("watch ended during a reload")
	}
	// Leaving for longer ends it.
	again()
	waitUserIMAP(t, func() bool { return len(s.idlePeers("alice")) == 0 })
}

// Online owners whose Inbox cannot be watched are polled every minute
// (shortened here) instead of at the regular interval.
func TestUserIMAPPollsOnlineOwnersWithoutWatchSoon(t *testing.T) {
	f := newUserStorageFixtureMode(t, true)
	s := newRoutedIMAPServer(t)
	f.useIMAPServer(t, s)
	release, err := f.imap.BeginActiveUserSession(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	if err := f.imap.Start(mail.UserIMAPBackgroundOptions{PollInterval: time.Hour, ScanInterval: 20 * time.Millisecond, DisableIDLE: true, OnlinePollInterval: 50 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	waitUserIMAP(t, func() bool { return f.messageCount(t, "alice") == 2 && f.messageCount(t, "bob") == 2 })
	s.mu.Lock()
	s.ownerUIDs["alice"] = []uint32{2, 1000000, 1000001}
	s.ownerUIDs["bob"] = []uint32{2, 1000000, 1000001}
	s.mu.Unlock()
	waitUserIMAP(t, func() bool { return f.messageCount(t, "alice") == 3 })
	time.Sleep(200 * time.Millisecond)
	if f.messageCount(t, "bob") != 2 {
		t.Fatal("away owner polled at the online interval")
	}
}

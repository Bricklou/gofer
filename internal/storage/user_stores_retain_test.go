package storage

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func storeOpen(m *UserStores, owner string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[owner]
	return ok && !entry.closing
}

func retainOwners(m *UserStores, owners ...string) func(...string) {
	var mu sync.Mutex
	set := map[string]bool{}
	update := func(owners ...string) {
		mu.Lock()
		defer mu.Unlock()
		set = map[string]bool{}
		for _, owner := range owners {
			set[owner] = true
		}
	}
	update(owners...)
	m.SetRetainHint(func(owner string) bool {
		mu.Lock()
		defer mu.Unlock()
		return set[owner]
	})
	return update
}

// When room is needed, the store of someone away is closed before an older
// one whose owner has Gofer open.
func TestUserStoresEvictOwnersAwayFirst(t *testing.T) {
	system := newUserStoreTestSystem(t)
	if _, err := system.Write().Exec(`INSERT INTO users(id, username, username_normalized) VALUES ('carol', 'carol', 'carol')`); err != nil {
		t.Fatal(err)
	}
	m := newUserStoreTestManager(t, system, UserStoreOptions{MaxOpen: 2, IdleTimeout: time.Hour})
	retainOwners(m, "alice")
	alice := acquireUserStore(t, m, "alice")
	alice.Release() // Longest idle, but online.
	bob := acquireUserStore(t, m, "bob")
	bob.Release()
	acquireUserStore(t, m, "carol")
	waitUserStorePhysicalClose(t, m, "bob")
	if !storeOpen(m, "alice") {
		t.Fatal("online owner's store closed instead of an away owner's")
	}
}

// If every idle store belongs to someone online, the longest idle is still
// closed, so new work never waits for a slot that will not free.
func TestUserStoresEvictOldestWhenEveryoneIsOnline(t *testing.T) {
	system := newUserStoreTestSystem(t)
	if _, err := system.Write().Exec(`INSERT INTO users(id, username, username_normalized) VALUES ('carol', 'carol', 'carol')`); err != nil {
		t.Fatal(err)
	}
	m := newUserStoreTestManager(t, system, UserStoreOptions{MaxOpen: 2, IdleTimeout: time.Hour})
	retainOwners(m, "alice", "bob")
	alice := acquireUserStore(t, m, "alice")
	alice.Release()
	bob := acquireUserStore(t, m, "bob")
	bob.Release()
	acquireUserStore(t, m, "carol")
	waitUserStorePhysicalClose(t, m, "alice")
	if !storeOpen(m, "bob") {
		t.Fatal("more recently used store closed")
	}
}

// Stores of people with Gofer open are not closed for being idle; once they
// leave, the idle timeout applies again.
func TestUserStoresKeepOnlineOwnersThroughIdleTimeout(t *testing.T) {
	m := newUserStoreTestManager(t, newUserStoreTestSystem(t), UserStoreOptions{IdleTimeout: time.Hour})
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	m.mu.Lock()
	m.now = func() time.Time { return time.Unix(0, now.Load()) }
	m.mu.Unlock()
	update := retainOwners(m, "alice")
	acquireUserStore(t, m, "alice").Release()
	acquireUserStore(t, m, "bob").Release()
	now.Add(int64(2 * time.Hour))
	m.reapIdle()
	waitUserStorePhysicalClose(t, m, "bob")
	if !storeOpen(m, "alice") {
		t.Fatal("online owner's idle store closed")
	}
	update() // Alice leaves.
	m.reapIdle()
	waitUserStorePhysicalClose(t, m, "alice")
}

package mail

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// Real-time notifications (IDLE) are for people who have Gofer open. Each
// watched folder holds one connection to the mail server for as long as it is
// watched, so watching every account of every user would not scale. When an
// owner opens Gofer their accounts are synced at once and their folders
// watched; when they close it the watches end after a grace period, and their
// mail is polled as usual. Online owners whose Inbox is not watched, because a
// limit was reached or the server cannot IDLE, are polled every minute.
const (
	// offlineGrace keeps watches through a page reload or a brief disconnect.
	offlineGrace = 2 * time.Minute
	// onlinePollInterval bounds how late new mail can appear for an online
	// owner whose Inbox is not watched.
	onlinePollInterval = time.Minute
	// defaultIdleWatchers bounds watched folders across all users.
	defaultIdleWatchers = 1024
	// defaultIdlePerServer bounds watched folders on one mail server.
	defaultIdlePerServer = 256
)

// ownerOnlineLocked reports whether the owner has Gofer open or closed it
// within the grace period. Caller holds mu.
func (s *UserIMAP) ownerOnlineLocked(owner string) bool {
	return s.onlineUsers[owner] > 0 || s.offlineTimers[owner] != nil
}

// ownerCameOnlineLocked runs when the owner's first tab connects. Caller
// holds mu.
func (s *UserIMAP) ownerCameOnlineLocked(owner string) {
	s.onlineSet.Store(owner, struct{}{})
	if timer := s.offlineTimers[owner]; timer != nil {
		// Back within the grace period: the watches are still running.
		timer.Stop()
		delete(s.offlineTimers, owner)
		return
	}
	if !s.background {
		return
	}
	go s.syncOwnerNow(owner)
}

// ownerWentOfflineLocked runs when the owner's last tab disconnects. Caller
// holds mu.
func (s *UserIMAP) ownerWentOfflineLocked(owner string) {
	if s.offlineTimers == nil {
		s.offlineTimers = make(map[string]*time.Timer)
	}
	if s.offlineTimers[owner] != nil {
		return
	}
	grace := s.backgroundOptions.IdleOfflineGrace
	if grace <= 0 {
		grace = offlineGrace
	}
	s.offlineTimers[owner] = time.AfterFunc(grace, func() { s.endOwnerWatches(owner) })
}

// endOwnerWatches stops an owner's watches once the grace period has passed
// without them coming back.
func (s *UserIMAP) endOwnerWatches(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.offlineTimers, owner)
	if s.onlineUsers[owner] > 0 {
		return
	}
	s.onlineSet.Delete(owner)
	for key, w := range s.watches {
		if w.owner == owner {
			w.suppressResync = true
			delete(s.watches, key)
			w.cancel()
		}
	}
}

// OwnerOnline reports, without taking the service's lock, whether the owner
// has Gofer open or closed it within the grace period. Storage uses it to keep
// these owners' databases open.
func (s *UserIMAP) OwnerOnline(owner string) bool {
	_, ok := s.onlineSet.Load(owner)
	return ok
}

// syncOwnerNow queues the owner's accounts ahead of background work, so a
// person opening Gofer sees current mail and gets their folders watched.
func (s *UserIMAP) syncOwnerNow(owner string) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	accounts, err := s.Routing().ListAccounts(ctx, owner, storage.AccountActive, "", 1000)
	if err != nil {
		if s.ctx.Err() == nil {
			log.Printf("user IMAP: sync accounts of an owner who opened Gofer: %v", err)
		}
		return
	}
	s.mu.Lock()
	for _, account := range accounts {
		_ = s.enqueueLocked(userIMAPJob{owner, account.AccountID}, true)
	}
	s.mu.Unlock()
	s.wakeBackground()
}

// pollIntervalFor returns how soon to poll an IMAP account again: within a
// minute when its owner is online but its Inbox is not being watched.
func (s *UserIMAP) pollIntervalFor(owner, account string, interval time.Duration) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ownerOnlineLocked(owner) || s.inboxWatchedLocked(account) {
		return interval
	}
	online := s.backgroundOptions.OnlinePollInterval
	if online <= 0 {
		online = onlinePollInterval
	}
	return min(interval, online)
}

// inboxWatchedLocked reports whether the account's Inbox watch is connected
// or connecting. Caller holds mu.
func (s *UserIMAP) inboxWatchedLocked(account string) bool {
	for key, w := range s.watches {
		if key.account == account && w.role == "inbox" && (w.status.Healthy || w.status.ReasonCode == "connecting") {
			return true
		}
	}
	return false
}

func watchHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}

// idleRoomLocked reports whether another folder on the host may be watched.
// When a limit is reached and the folder is an Inbox, it stops a watch of a
// non-Inbox folder instead and reports freeing: that slot is released once
// the watch has ended, so the caller should retry on a follow-up sync.
// Caller holds mu.
func (s *UserIMAP) idleRoomLocked(host, role string) (room, freeing bool) {
	globalFull := s.idleCount >= s.backgroundOptions.MaxIdleWatchers
	hostFull := s.idleByHost[host] >= s.maxIdlePerServer
	if !globalFull && !hostFull {
		return true, false
	}
	if role != "inbox" {
		return false, false
	}
	for key, w := range s.watches {
		if w.role == "inbox" || (hostFull && w.host != host) {
			continue
		}
		// Its account falls back to polling until room is found again.
		delete(s.watches, key)
		w.cancel()
		return false, true
	}
	return false, false
}

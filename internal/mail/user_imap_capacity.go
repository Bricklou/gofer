package mail

import (
	"context"
	"log"
	"strings"
	"time"
)

// Background sync capacity. Workers run account syncs taken from two queues:
// accounts of owners with Gofer open come first. A worker mostly waits on
// the mail server, so the bound is set by how hard Gofer may press mail
// servers and how many user databases stay open, not by CPU.
const (
	defaultSyncWorkers = 16
	// syncWorkersBeforeStart serves syncs requested before the background
	// service starts; Start sets the configured number.
	syncWorkersBeforeStart = 4
	maxSyncWorkers         = 1024
	defaultSyncsPerServer  = 8
	// interactiveSlots bounds work people wait for, such as opening a
	// message, across all users. It is separate from background syncs so
	// that a busy sync backlog never delays it.
	interactiveSlots = 32
	// calendarServiceSlots bounds calendar provider calls at once: up to 16
	// from background work, with room left for what people do themselves.
	calendarServiceSlots = 24
	syncQueueSize        = 1024
	// syncLateAfter is how long a due sync may wait for a worker before
	// Gofer warns that background capacity is short.
	syncLateAfter = 2 * time.Minute
	// syncLateWarnEvery limits those warnings.
	syncLateWarnEvery = 10 * time.Minute
)

// queueFor picks the queue for an owner's account: people with Gofer open
// are served first. Caller holds mu.
func (s *UserIMAP) queueFor(owner string) chan userIMAPJob {
	if s.onlineUsers[owner] > 0 {
		return s.queueOnline
	}
	return s.queue
}

// setSyncWorkersLocked starts workers up to n. When n is lower than the
// running count, workers above it stop after their current or next job.
// Caller holds mu.
func (s *UserIMAP) setSyncWorkersLocked(n int) {
	s.workerLimit = n
	for s.workerCount < n {
		index := s.workerCount
		s.workerCount++
		s.workers.Add(1)
		go s.work(index)
	}
}

func (s *UserIMAP) workerRetired(index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < s.workerLimit {
		return false
	}
	s.workerCount--
	return true
}

// nextJob waits for a job, taking queued online owners' accounts first.
func (s *UserIMAP) nextJob() (userIMAPJob, bool) {
	select {
	case job := <-s.queueOnline:
		return job, true
	default:
	}
	select {
	case <-s.ctx.Done():
		return userIMAPJob{}, false
	case job := <-s.queueOnline:
		return job, true
	case job := <-s.queue:
		return job, true
	}
}

// startJobLocked marks the job running and reports how long it waited.
// Caller holds mu.
func (s *UserIMAP) startJobLocked(job userIMAPJob) {
	state := s.queued[job.account]
	if state == nil {
		return
	}
	state.running = true
	if state.queuedAt.IsZero() {
		return
	}
	if waited := time.Since(state.queuedAt); waited > syncLateAfter {
		s.warnLateLocked("a mail sync waited " + waited.Round(time.Second).String() + " for a free worker")
	}
}

// noteQueueFullLocked warns that due syncs could not even be queued.
// Caller holds mu.
func (s *UserIMAP) noteQueueFullLocked() {
	s.warnLateLocked("the mail sync queue is full")
}

func (s *UserIMAP) warnLateLocked(reason string) {
	if time.Since(s.lastLateWarning) < syncLateWarnEvery {
		return
	}
	s.lastLateWarning = time.Now()
	log.Printf("user IMAP: %s (%d accounts waiting for %d workers). If this persists, raise GOFER_SYNC_MAX_CONCURRENT.",
		reason, len(s.queue)+len(s.queueOnline), s.workerLimit)
}

type serverSlot struct {
	tokens chan struct{}
	refs   int
}

// acquireServer waits until fewer than MaxPerServer background syncs talk to
// the host and returns the release function.
func (s *UserIMAP) acquireServer(ctx context.Context, host string) (func(), error) {
	host = strings.ToLower(strings.TrimSpace(host))
	s.mu.Lock()
	limit := s.maxPerServer
	if limit <= 0 {
		limit = defaultSyncsPerServer
	}
	slot := s.serverSlots[host]
	if slot == nil {
		slot = &serverSlot{tokens: make(chan struct{}, limit)}
		s.serverSlots[host] = slot
	}
	slot.refs++
	s.mu.Unlock()
	leave := func() {
		s.mu.Lock()
		slot.refs--
		if slot.refs == 0 && s.serverSlots[host] == slot {
			delete(s.serverSlots, host)
		}
		s.mu.Unlock()
	}
	select {
	case slot.tokens <- struct{}{}:
		return func() { <-slot.tokens; leave() }, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

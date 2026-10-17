package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// Mailbox exports and imports run as background jobs. Each job keeps its
// record and files in its own directory, so it survives a restart and
// continues where it stopped.
const (
	mailJobReview   = "review"
	mailJobRunning  = "running"
	mailJobPaused   = "paused"
	mailJobDone     = "done"
	mailJobCanceled = "canceled"

	// Finished jobs are removed after a week, and uploads nobody started
	// importing after a day.
	mailJobKeep       = 7 * 24 * time.Hour
	mailJobReviewKeep = 24 * time.Hour
	// At most this many jobs of a kind work at once, across all users.
	mailJobWorkers = 2
)

var errMailJobCanceled = errors.New("job canceled")

// mailJobInfo is what every job records about itself.
type mailJobInfo struct {
	ID         string    `json:"id"`
	Owner      string    `json:"owner"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

func (i *mailJobInfo) info() *mailJobInfo { return i }

type mailJob interface {
	info() *mailJobInfo
}

type mailJobManager[J mailJob] struct {
	name  string
	dir   string
	ctx   context.Context
	slots chan struct{}
	fresh func() J
	run   func(context.Context, J)

	mu      sync.Mutex
	running map[string]context.CancelFunc
	// active holds the running jobs that are working, not waiting for a
	// free slot.
	active map[string]bool
	// files serializes reads and writes of job records.
	files sync.Mutex
	wg    sync.WaitGroup
}

func mailJobDirectory(db *storage.DB, name string) string {
	return filepath.Join(filepath.Dir(db.Path()), name)
}

// newMailJobManager starts the jobs that were running when Gofer stopped.
func newMailJobManager[J mailJob](ctx context.Context, name, dir string, fresh func() J, run func(context.Context, J)) *mailJobManager[J] {
	m := &mailJobManager[J]{name: name, dir: dir, ctx: ctx, slots: make(chan struct{}, mailJobWorkers), fresh: fresh, run: run,
		running: map[string]context.CancelFunc{}, active: map[string]bool{}}
	jobs, err := m.jobs("")
	if err != nil {
		log.Printf("%s: list jobs: %v", name, err)
	}
	for _, job := range jobs {
		if job.info().State == mailJobRunning {
			m.start(job)
		}
	}
	go func() {
		<-ctx.Done()
		m.wg.Wait()
	}()
	return m
}

func newMailJobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func validMailJobID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (m *mailJobManager[J]) jobDir(id string) string { return filepath.Join(m.dir, id) }

func (m *mailJobManager[J]) load(id string) (J, error) {
	var none J
	if !validMailJobID(id) {
		return none, os.ErrNotExist
	}
	m.files.Lock()
	defer m.files.Unlock()
	data, err := os.ReadFile(filepath.Join(m.jobDir(id), "job.json"))
	if err != nil {
		return none, err
	}
	job := m.fresh()
	if err := json.Unmarshal(data, job); err != nil {
		return none, err
	}
	if job.info().ID != id {
		return none, os.ErrNotExist
	}
	return job, nil
}

// loadOwned returns the owner's job, or os.ErrNotExist for anyone else's.
func (m *mailJobManager[J]) loadOwned(owner, id string) (J, error) {
	job, err := m.load(id)
	if err != nil {
		return job, err
	}
	if job.info().Owner != owner {
		var none J
		return none, os.ErrNotExist
	}
	return job, nil
}

func (m *mailJobManager[J]) save(job J) error {
	m.files.Lock()
	defer m.files.Unlock()
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	dir := m.jobDir(job.info().ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp := filepath.Join(dir, "job.json.tmp")
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(dir, "job.json"))
}

// jobs lists jobs, newest first, for owner or for everyone when owner is
// empty. Jobs past their time are removed on the way.
func (m *mailJobManager[J]) jobs(owner string) ([]J, error) {
	entries, err := os.ReadDir(m.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []J
	for _, entry := range entries {
		if !entry.IsDir() || !validMailJobID(entry.Name()) {
			continue
		}
		job, err := m.load(entry.Name())
		if err != nil {
			continue
		}
		info := job.info()
		if owner != "" && info.Owner != owner {
			continue
		}
		keep := mailJobKeep
		if info.State == mailJobReview {
			keep = mailJobReviewKeep
		}
		if info.State != mailJobRunning && !m.isRunning(info.ID) && time.Since(info.UpdatedAt) > keep {
			_ = os.RemoveAll(m.jobDir(info.ID))
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].info().CreatedAt.After(jobs[k].info().CreatedAt) })
	return jobs, nil
}

func (m *mailJobManager[J]) isRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, running := m.running[id]
	return running
}

// isWaiting reports whether a running job waits for others to finish.
func (m *mailJobManager[J]) isWaiting(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.active[id]
}

func (m *mailJobManager[J]) start(job J) {
	id := job.info().ID
	m.mu.Lock()
	if _, running := m.running[id]; running || m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancelCause(m.ctx)
	m.running[id] = func() { cancel(errMailJobCanceled) }
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() {
			m.mu.Lock()
			delete(m.running, id)
			delete(m.active, id)
			m.mu.Unlock()
			cancel(nil)
		}()
		select {
		case m.slots <- struct{}{}:
		case <-ctx.Done():
			m.stopped(ctx, job)
			return
		}
		defer func() { <-m.slots }()
		m.mu.Lock()
		m.active[id] = true
		m.mu.Unlock()
		m.run(ctx, job)
	}()
}

// cancel stops a running job and waits until it has stopped.
func (m *mailJobManager[J]) cancel(id string) {
	m.mu.Lock()
	stop := m.running[id]
	m.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
	for m.isRunning(id) {
		time.Sleep(20 * time.Millisecond)
	}
}

// stopped records a job that stopped before finishing. One canceled by its
// owner is canceled; one stopped by Gofer shutting down stays running, so it
// continues on the next start.
func (m *mailJobManager[J]) stopped(ctx context.Context, job J) {
	info := job.info()
	if errors.Is(context.Cause(ctx), errMailJobCanceled) {
		info.State = mailJobCanceled
		info.FinishedAt = time.Now().UTC()
	}
	info.UpdatedAt = time.Now().UTC()
	if err := m.save(job); err != nil {
		log.Printf("%s %s: save: %v", m.name, info.ID, err)
	}
}

// finish records how a job's run ended: done, paused with the reason, or
// stopped.
func (m *mailJobManager[J]) finish(ctx context.Context, job J, err error, describe func(error) string) {
	info := job.info()
	switch {
	case ctx.Err() != nil:
		m.stopped(ctx, job)
		return
	case err != nil:
		log.Printf("%s %s: %v", m.name, info.ID, err)
		info.State = mailJobPaused
		info.Error = describe(err)
	default:
		info.State = mailJobDone
		info.Error = ""
		info.FinishedAt = time.Now().UTC()
	}
	info.UpdatedAt = time.Now().UTC()
	if err := m.save(job); err != nil {
		log.Printf("%s %s: save: %v", m.name, info.ID, err)
	}
}

// cancelJob cancels a job: a running one stops, and one that was waiting to
// continue after a restart is marked canceled.
func (m *mailJobManager[J]) cancelJob(job J) {
	info := job.info()
	if m.isRunning(info.ID) {
		m.cancel(info.ID)
		return
	}
	if info.State == mailJobRunning {
		info.State = mailJobCanceled
		info.FinishedAt = time.Now().UTC()
		info.UpdatedAt = info.FinishedAt
		if err := m.save(job); err != nil {
			log.Printf("%s %s: save: %v", m.name, info.ID, err)
		}
	}
}

// resume continues a paused job.
func (m *mailJobManager[J]) resume(job J) {
	info := job.info()
	if info.State != mailJobPaused || m.isRunning(info.ID) {
		return
	}
	info.State = mailJobRunning
	info.Error = ""
	info.UpdatedAt = time.Now().UTC()
	if err := m.save(job); err != nil {
		log.Printf("%s %s: save: %v", m.name, info.ID, err)
		return
	}
	m.start(job)
}

// remove stops a job and deletes it with its files.
func (m *mailJobManager[J]) remove(job J) {
	id := job.info().ID
	m.cancel(id)
	if err := os.RemoveAll(m.jobDir(id)); err != nil {
		log.Printf("%s %s: delete: %v", m.name, id, err)
	}
}

// mailJobRetryDelay is the wait before the first retry of a message; each
// later retry waits longer.
var mailJobRetryDelay = 2 * time.Second

// retryMailJobStep runs step up to three times while it fails with an error
// worth retrying.
func retryMailJobStep(ctx context.Context, step func() error, final func(error) bool) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * mailJobRetryDelay):
			}
		}
		err = step()
		if err == nil || final(err) || ctx.Err() != nil {
			return err
		}
	}
	return err
}

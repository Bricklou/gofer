package handler

import (
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

// A calendar's mark is what it was like when last downloaded; checks move
// only its time, and calendars no longer synced lose theirs.
func TestCalendarChangeMarks(t *testing.T) {
	var marks calendarChangeMarks
	work := storage.CalendarSource{ID: "work", UserID: "alice"}
	home := storage.CalendarSource{ID: "home", UserID: "alice"}
	other := storage.CalendarSource{ID: "work", UserID: "bob"}
	began := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	marks.checked(work, began)
	if _, ok := marks.get(work); ok {
		t.Fatal("checked before a download")
	}
	marks.downloaded(work, "ctag:1", began)
	marks.downloaded(home, "", began)
	marks.downloaded(other, "ctag:9", began)
	marks.checked(work, began.Add(time.Minute))
	if mark, ok := marks.get(work); !ok || mark.tag != "ctag:1" || !mark.since.Equal(began) || !mark.checked.Equal(began.Add(time.Minute)) {
		t.Fatalf("work: %+v %v", mark, ok)
	}
	marks.forget("alice", []storage.CalendarSource{work})
	if _, ok := marks.get(home); ok {
		t.Fatal("unselected calendar kept")
	}
	if _, ok := marks.get(other); !ok {
		t.Fatal("another owner's calendar forgotten")
	}
}

// Workers are added while more accounts wait than workers are free, up to
// the most allowed.
func TestUserCalendarWorkersGrowWithBacklog(t *testing.T) {
	w := &userCalendarWorkers{options: UserCalendarSyncOptions{MinWorkers: 2, MaxWorkers: 4}, queue: make(chan userCalendarJob, 8), workers: 2, idle: 2}
	started := 0
	start := func(extra bool) {
		if !extra {
			t.Fatal("backlog worker started as a lasting one")
		}
		started++
	}
	w.queue <- userCalendarJob{owner: "alice", account: "one"}
	w.queue <- userCalendarJob{owner: "alice", account: "two"}
	if w.grow(start); started != 0 {
		t.Fatalf("grew with idle workers: %d", started)
	}
	for _, account := range []string{"three", "four", "five", "six"} {
		w.queue <- userCalendarJob{owner: "bob", account: account}
	}
	if w.grow(start); started != 2 || w.workers != 4 || w.idle != 4 {
		t.Fatalf("grew to %d workers (%d idle), started %d", w.workers, w.idle, started)
	}
}

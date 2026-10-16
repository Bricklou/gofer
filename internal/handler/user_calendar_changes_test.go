package handler

import (
	"encoding/json"
	"errors"
	"strings"
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

// Google and Outlook are asked for events changed since a little before the
// last download; CalDAV for its change tag.
func TestCalendarChanged(t *testing.T) {
	mark := calendarChangeMark{tag: "ctag:1", since: time.Date(2026, 10, 2, 9, 0, 10, 0, time.UTC)}
	var asked string
	answer := func(found bool) func(string, any) error {
		return func(endpoint string, out any) error {
			asked = endpoint
			body := `{"items":[],"value":[]}`
			if found {
				body = `{"items":[{"id":"e"}],"value":[{"id":"e"}]}`
			}
			return json.Unmarshal([]byte(body), out)
		}
	}
	noTag := func() (string, error) { t.Fatal("CalDAV asked for a provider calendar"); return "", nil }
	for _, provider := range []string{"gmail", "outlook"} {
		source := storage.CalendarSource{Provider: provider, RemoteID: "work"}
		if changed, _, err := calendarChanged(source, mark, answer(false), noTag); err != nil || changed {
			t.Fatalf("%s unchanged: %v %v", provider, changed, err)
		}
		if !strings.Contains(asked, "2026-10-02T09%3A00%3A00Z") {
			t.Fatalf("%s asked from %s", provider, asked)
		}
		if changed, _, err := calendarChanged(source, mark, answer(true), noTag); err != nil || !changed {
			t.Fatalf("%s changed: %v %v", provider, changed, err)
		}
	}
	dav := storage.CalendarSource{Provider: storage.CalendarSourceProviderCalDAV}
	noJSON := func(string, any) error { t.Fatal("provider asked for a CalDAV calendar"); return nil }
	for tag, want := range map[string]bool{"ctag:1": false, "ctag:2": true, "": false} {
		if changed, got, err := calendarChanged(dav, mark, noJSON, func() (string, error) { return tag, nil }); err != nil || changed != want || got != tag {
			t.Errorf("tag %q: %v %q %v", tag, changed, got, err)
		}
	}
	if changed, _, err := calendarChanged(dav, mark, noJSON, func() (string, error) { return "", errors.New("offline") }); err == nil || changed {
		t.Fatal("unreadable tag counted as a change")
	}
}

func TestCalendarChangeDue(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	later := now.Add(4 * time.Minute)
	mark := calendarChangeMark{checked: now.Add(-time.Minute)}
	source := storage.CalendarSource{SyncState: "ok", NextAttemptAt: &later}
	if !calendarChangeDue(source, mark, true, now) {
		t.Fatal("not checked after a minute")
	}
	recent := calendarChangeMark{checked: now.Add(-20 * time.Second)}
	syncing := source
	syncing.SyncState = "syncing"
	due := source
	due.NextAttemptAt = &now
	for name, test := range map[string]struct {
		source storage.CalendarSource
		mark   calendarChangeMark
		known  bool
	}{"never downloaded": {source, mark, false}, "checked recently": {source, recent, true}, "downloading": {syncing, mark, true}, "due for refresh": {due, mark, true}} {
		if calendarChangeDue(test.source, test.mark, test.known, now) {
			t.Errorf("%s: checked", name)
		}
	}
}

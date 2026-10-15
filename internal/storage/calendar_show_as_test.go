package storage

import (
	"path/filepath"
	"testing"
	"time"
)

// An event keeps how it marks its time; unknown values are not stored.
func TestCalendarShowAsRoundTrip(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "show-as.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','outlook','owner@example.com');
INSERT INTO calendar_sources(id,user_id,account_id,provider,remote_id,is_selected) VALUES('source','owner','account','outlook','calendar',1)`); err != nil {
		t.Fatal(err)
	}
	start, end := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for value, want := range map[string]string{"free": "free", "working_elsewhere": "working_elsewhere", "maybe": ""} {
		event := CalendarEvent{ID: "event", RemoteID: "remote", ETag: `"v1"`, Summary: "Review", StartAt: &start, EndAt: &end, ShowAs: value}
		if err := db.ReplaceCalendarEvents(t.Context(), "owner", "source", []CalendarEvent{event}, start.Add(-time.Hour), end.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		saved, err := db.GetCalendarEvent(t.Context(), "owner", "event")
		if err != nil || saved.ShowAs != want {
			t.Fatalf("%q stored as %q (%v)", value, saved.ShowAs, err)
		}
	}
}

// Events cached before keep working and have no show-as until they sync.
func TestCalendarShowAsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "show-as.db")
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','gmail','owner@example.com');
INSERT INTO calendar_sources(id,user_id,account_id,provider,remote_id,is_selected) VALUES('source','owner','account','gmail','primary',1);
INSERT INTO calendar_events(id,user_id,source_id,remote_id,etag,summary,all_day,start_at,end_at) VALUES('old','owner','source','old-remote','"v1"','Planning',0,'2026-11-03 09:00:00','2026-11-03 10:00:00');
ALTER TABLE calendar_events DROP COLUMN show_as;
DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(109)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err = New(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	old, err := db.GetCalendarEvent(t.Context(), "owner", "old")
	if err != nil || old.ShowAs != "" {
		t.Fatalf("migrated event: %q %v", old.ShowAs, err)
	}
}

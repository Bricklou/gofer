package storage

import (
	"path/filepath"
	"testing"
	"time"
)

// Events cached before reminders keep working and have none until resynced.
func TestCalendarReminderMigrationAndRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reminders.db")
	db, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','gmail','owner@example.com');
INSERT INTO calendar_sources(id,user_id,account_id,provider,remote_id,is_selected) VALUES('source','owner','account','gmail','primary',1);
INSERT INTO calendar_events(id,user_id,source_id,remote_id,etag,summary,all_day,start_at,end_at) VALUES('old','owner','source','old-remote','"v1"','Planning',0,'2026-11-03 09:00:00','2026-11-03 10:00:00');
ALTER TABLE calendar_events DROP COLUMN reminder_minutes;
DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(108)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if db, err = New(path); err != nil {
		t.Fatal(err)
	}
	old, err := db.GetCalendarEvent(t.Context(), "owner", "old")
	if err != nil || old.ReminderMinutes != nil {
		t.Fatalf("migrated event: %+v %v", old.ReminderMinutes, err)
	}
	start, end := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for _, minutes := range []int{0, 15} {
		value := minutes
		event := CalendarEvent{ID: "new", RemoteID: "new-remote", ETag: `"v1"`, Summary: "Review", StartAt: &start, EndAt: &end, ReminderMinutes: &value}
		if err := db.ReplaceCalendarEvents(t.Context(), "owner", "source", []CalendarEvent{event}, start.Add(-time.Hour), end.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		saved, err := db.GetCalendarEvent(t.Context(), "owner", "new")
		if err != nil || saved.ReminderMinutes == nil || *saved.ReminderMinutes != minutes {
			t.Fatalf("reminder %d stored as %v (%v)", minutes, saved.ReminderMinutes, err)
		}
	}
}

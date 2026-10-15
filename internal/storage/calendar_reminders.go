package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// migrateV108ToV109 adds each cached event's reminder: minutes before it
// starts, or -1 when it has none.
func migrateV108ToV109(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 109 {
		return nil
	}
	exists, err := tableExistsTx(tx, "calendar_events")
	if err != nil {
		return err
	}
	if exists {
		present, err := columnExistsTx(tx, "calendar_events", "reminder_minutes")
		if err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(`ALTER TABLE calendar_events ADD COLUMN reminder_minutes INTEGER NOT NULL DEFAULT -1 CHECK (reminder_minutes >= -1)`); err != nil {
				return err
			}
		}
	}
	if err := markSchemaVersion(tx, 109); err != nil {
		return err
	}
	return tx.Commit()
}

// CalendarReminderDue is a cached event whose reminder falls due.
type CalendarReminderDue struct {
	Event  CalendarEvent
	FireAt time.Time
}

// ListDueCalendarReminders returns a user's timed events on shown calendars
// whose reminder falls in (from, to]. Cancelled and declined events, and
// all-day ones, have none.
func (db *DB) ListDueCalendarReminders(ctx context.Context, userID string, from, to time.Time) ([]CalendarReminderDue, error) {
	if strings.TrimSpace(userID) == "" || !to.After(from) {
		return nil, nil
	}
	rows, err := db.Read().QueryContext(ctx, calendarEventSelect+`
		  AND event.all_day = 0 AND event.reminder_minutes >= 0 AND source.is_hidden = 0
		  AND event.status <> 'cancelled' AND lower(event.response_status) <> 'declined'
		  AND datetime(event.start_at, '-' || event.reminder_minutes || ' minutes') > datetime(?)
		  AND datetime(event.start_at, '-' || event.reminder_minutes || ' minutes') <= datetime(?)
		ORDER BY event.start_at, event.id LIMIT 200`, userID, formatDBTime(from.UTC()), formatDBTime(to.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []CalendarReminderDue
	for rows.Next() {
		event, err := scanCalendarEvent(rows)
		if err != nil {
			return nil, err
		}
		if event.StartAt == nil || event.ReminderMinutes == nil {
			continue
		}
		due = append(due, CalendarReminderDue{Event: event, FireAt: event.StartAt.Add(-time.Duration(*event.ReminderMinutes) * time.Minute)})
	}
	return due, rows.Err()
}

// CalendarReminderOwners lists the users with any cached event reminder.
func (db *DB) CalendarReminderOwners(ctx context.Context) ([]string, error) {
	rows, err := db.Read().QueryContext(ctx, `SELECT DISTINCT user_id FROM calendar_events WHERE reminder_minutes >= 0 AND is_deleted = 0 AND all_day = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

// CalendarOwners lists the users with an active account that has calendars.
func (r *AccountRouting) CalendarOwners(ctx context.Context) ([]string, error) {
	rows, err := r.System().Read().QueryContext(ctx, `SELECT DISTINCT d.user_id FROM gofer_account_directory d
		JOIN gofer_account_service_schedule s ON s.account_id = d.account_id
		WHERE s.service = 'calendar' AND d.state = 'active' ORDER BY d.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

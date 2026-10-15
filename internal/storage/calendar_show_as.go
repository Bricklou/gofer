package storage

import (
	"database/sql"
	"strings"
)

// migrateV109ToV110 adds how each cached event marks its time. Events cached
// before have none until they sync again.
func migrateV109ToV110(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 110 {
		return nil
	}
	exists, err := tableExistsTx(tx, "calendar_events")
	if err != nil {
		return err
	}
	if exists {
		present, err := columnExistsTx(tx, "calendar_events", "show_as")
		if err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(`ALTER TABLE calendar_events ADD COLUMN show_as TEXT NOT NULL DEFAULT '' CHECK (show_as IN ('', 'busy', 'free', 'tentative', 'oof', 'working_elsewhere'))`); err != nil {
				return err
			}
		}
	}
	if err := markSchemaVersion(tx, 110); err != nil {
		return err
	}
	return tx.Commit()
}

// CalendarShowAsValue returns a stored show-as value, or empty when it isn't
// one Gofer knows.
func CalendarShowAsValue(value string) string {
	switch value = strings.TrimSpace(value); value {
	case "busy", "free", "tentative", "oof", "working_elsewhere":
		return value
	}
	return ""
}

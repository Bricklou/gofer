package storage

import "database/sql"

// migrateV113ToV114 adds each account's JMAP session URL. It stays empty for
// IMAP, Gmail and Outlook accounts.
func migrateV113ToV114(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 114 {
		return nil
	}
	exists, err := tableExistsTx(tx, "accounts")
	if err != nil {
		return err
	}
	if exists {
		present, err := columnExistsTx(tx, "accounts", "jmap_session_url")
		if err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(`ALTER TABLE accounts ADD COLUMN jmap_session_url TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	if err := markSchemaVersion(tx, 114); err != nil {
		return err
	}
	return tx.Commit()
}

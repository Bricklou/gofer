package storage

import "database/sql"

// migrateV119ToV120 adds message_folder_copies. A folder state belongs to one
// provider copy, so when two copies of a message sit in one folder, such as
// both copies of mail sent to oneself in Trash, the other copy is recorded
// there. It starts empty: states that already took over one copy's id hold
// the other until a full sync finds both again.
func migrateV119ToV120(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 120 {
		return nil
	}
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS message_folder_copies (
			message_id INTEGER NOT NULL,
			folder_id TEXT NOT NULL,
			remote_message_id TEXT NOT NULL CHECK (remote_message_id != ''),
			PRIMARY KEY (message_id, folder_id, remote_message_id),
			FOREIGN KEY (message_id, folder_id) REFERENCES message_folder_state(message_id, folder_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_message_folder_copies_remote_message
		ON message_folder_copies(remote_message_id);`); err != nil {
		return err
	}
	if err := markSchemaVersion(tx, 120); err != nil {
		return err
	}
	return tx.Commit()
}

package storage

import "database/sql"

// migrateV117ToV118 gives each folder state the provider id of the copy it
// belongs to. Gmail, Outlook and JMAP can hold two messages with one
// Message-ID, such as the Sent and Inbox copies of mail sent to oneself, and
// those share a row; the row's single id let each sync take the other copy
// over. The column starts as the row's id. Rows already merged keep that one
// id until the next full sync splits them.
func migrateV117ToV118(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 118 {
		return nil
	}
	exists, err := tableExistsTx(tx, "message_folder_state")
	if err != nil {
		return err
	}
	if exists {
		hasColumn, err := columnExistsTx(tx, "message_folder_state", "remote_message_id")
		if err != nil {
			return err
		}
		if !hasColumn {
			if _, err := tx.Exec(`ALTER TABLE message_folder_state ADD COLUMN remote_message_id TEXT`); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_message_folder_state_remote_message
			ON message_folder_state(remote_message_id) WHERE remote_message_id IS NOT NULL`); err != nil {
			return err
		}
		// Schemas built after 119 have no row id to copy.
		rowIDs, err := columnExistsTx(tx, "messages", "remote_message_id")
		if err != nil {
			return err
		}
		accounts, err := tableExistsTx(tx, "accounts")
		if err != nil {
			return err
		}
		if rowIDs && accounts {
			if _, err := tx.Exec(folderStateProviderIDBackfillSQL); err != nil {
				return err
			}
		}
	}
	if err := markSchemaVersion(tx, 118); err != nil {
		return err
	}
	return tx.Commit()
}

// folderStateProviderIDBackfillSQL gives the folder states of provider-synced
// rows the row's provider id.
const folderStateProviderIDBackfillSQL = `UPDATE message_folder_state
	SET remote_message_id = (SELECT m.remote_message_id FROM messages m WHERE m.id = message_folder_state.message_id)
	WHERE remote_message_id IS NULL
	  AND message_id IN (
	    SELECT m.id FROM messages m JOIN accounts a ON a.id = m.account_id
	    WHERE a.provider IN ('gmail', 'outlook', 'jmap') AND COALESCE(m.remote_message_id, '') != ''
	  )`

// copiedFolderStateProviderIDBackfillSQL is folderStateProviderIDBackfillSQL
// for folder states copied into a per-user database, whose rows have no
// provider id; it is read from the source rows.
const copiedFolderStateProviderIDBackfillSQL = `UPDATE main.message_folder_state
	SET remote_message_id = (SELECT m.remote_message_id FROM migration_source.messages m WHERE m.id = message_folder_state.message_id)
	WHERE remote_message_id IS NULL
	  AND message_id IN (
	    SELECT m.id FROM migration_source.messages m JOIN migration_source.accounts a ON a.id = m.account_id
	    WHERE a.provider IN ('gmail', 'outlook', 'jmap') AND COALESCE(m.remote_message_id, '') != ''
	  )`

// migrateV118ToV119 drops the provider id kept on message rows. Since schema
// 118 every copy keeps its own on its folder states, and the row's named only
// one of them.
func migrateV118ToV119(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 119 {
		return nil
	}
	exists, err := tableExistsTx(tx, "messages")
	if err != nil {
		return err
	}
	if exists {
		hasColumn, err := columnExistsTx(tx, "messages", "remote_message_id")
		if err != nil {
			return err
		}
		if hasColumn {
			if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_messages_remote_id`); err != nil {
				return err
			}
			if _, err := tx.Exec(`ALTER TABLE messages DROP COLUMN remote_message_id`); err != nil {
				return err
			}
		}
	}
	if err := markSchemaVersion(tx, 119); err != nil {
		return err
	}
	return tx.Commit()
}

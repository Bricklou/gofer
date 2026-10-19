package storage

import "database/sql"

// migrateV115ToV116 lets queued message changes belong to JMAP accounts.
// SQLite cannot change a CHECK constraint in place, so the table is rebuilt
// with all its rows and indexes. The rows wait in a temporary table rather
// than the old table being renamed: a rename validates the whole schema,
// which older partial databases fail.
func migrateV115ToV116(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 116 {
		return nil
	}
	exists, err := tableExistsTx(tx, "message_mutations")
	if err != nil {
		return err
	}
	if exists {
		const columns = `id, account_id, message_id, folder_id, provider_type, kind, target_value, destination_folder_id,
			source_uid_validity, status, attempt_count, last_error, locked_at, next_attempt_at, created_at, updated_at`
		var queued int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM message_mutations`).Scan(&queued); err != nil {
			return err
		}
		statements := []string{
			`DROP TABLE IF EXISTS temp.message_mutations_v115`,
			`CREATE TEMP TABLE message_mutations_v115 AS SELECT ` + columns + ` FROM message_mutations`,
			`DROP TABLE message_mutations`,
			messageMutationsTableSQL,
		}
		// Copying checks the rows' messages, so skip it when there is nothing
		// to copy; older partial databases may have no messages table.
		if queued > 0 {
			statements = append(statements, `INSERT INTO message_mutations (`+columns+`) SELECT `+columns+` FROM temp.message_mutations_v115`)
		}
		statements = append(statements,
			`DROP TABLE temp.message_mutations_v115`,
			`CREATE INDEX IF NOT EXISTS idx_message_mutations_due ON message_mutations(status, next_attempt_at, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_message_mutations_account ON message_mutations(account_id, status, next_attempt_at)`,
		)
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	if err := markSchemaVersion(tx, 116); err != nil {
		return err
	}
	return tx.Commit()
}

// messageMutationsTableSQL matches the table in schema.sql.
const messageMutationsTableSQL = `CREATE TABLE message_mutations (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
	folder_id TEXT NOT NULL DEFAULT '',
	provider_type TEXT NOT NULL CHECK (provider_type IN ('gmail', 'outlook', 'imap', 'jmap')),
	kind TEXT NOT NULL CHECK (kind IN ('read', 'starred', 'move', 'delete')),
	target_value INTEGER NOT NULL CHECK (target_value IN (0, 1)),
	destination_folder_id TEXT NOT NULL DEFAULT '',
	source_uid_validity INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'failed', 'applied')),
	attempt_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	locked_at DATETIME,
	next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(message_id, kind, folder_id)
)`

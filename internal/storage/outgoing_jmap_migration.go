package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// outgoingSendColumns lists outgoing_sends' columns in table order.
const outgoingSendColumns = `id, account_id, message_id, draft_id, transport, envelope_from, envelope_recipients, mime_data,
	message_json, send_after, next_attempt_at, is_scheduled, status, attempt_count, last_error, locked_at, sent_message_id,
	sent_copy_status, sent_copy_attempt_count, sent_copy_last_error, sent_copy_locked_at, sent_copy_next_attempt_at,
	sent_copy_uid, sent_copy_uid_validity, created_at, updated_at`

// migrateV116ToV117 lets queued outgoing mail be sent over JMAP. SQLite cannot
// change a CHECK constraint in place, so outgoing_sends is rebuilt. Pending
// calendar replies and provider send receipts reference it with ON DELETE
// CASCADE, so the rebuild runs with foreign keys off, like the other table
// rebuilds: renaming the old table leaves those references on the name, and
// dropping it removes nothing else. The references are checked before the
// change commits.
func migrateV116ToV117(db *sql.DB) (returnErr error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()
	var version int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 117 {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA legacy_alter_table = ON`); err != nil {
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
		return fmt.Errorf("enable legacy alter table mode: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, `PRAGMA legacy_alter_table = OFF`); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("disable legacy alter table mode: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("restore foreign keys: %w", err)
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Only a database with accounts and messages can hold outgoing mail; older
	// partial databases keep their table as it is.
	rebuild := true
	for _, table := range []string{"outgoing_sends", "accounts", "messages"} {
		exists, err := tableExistsTx(tx, table)
		if err != nil {
			return err
		}
		rebuild = rebuild && exists
	}
	if rebuild {
		if err := rebuildOutgoingSendsTx(tx); err != nil {
			return err
		}
	}
	if err := markSchemaVersion(tx, 117); err != nil {
		return err
	}
	return tx.Commit()
}

// outgoingSendFillIns are the values for NOT NULL columns that databases
// upgraded through older versions added as nullable.
var outgoingSendFillIns = map[string]string{
	"next_attempt_at":           "COALESCE(next_attempt_at, send_after, CURRENT_TIMESTAMP)",
	"sent_copy_next_attempt_at": "COALESCE(sent_copy_next_attempt_at, CURRENT_TIMESTAMP)",
}

func rebuildOutgoingSendsTx(tx *sql.Tx) error {
	columns, selected, err := sharedOutgoingSendColumnsTx(tx)
	if err != nil {
		return err
	}
	var rows int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM outgoing_sends`).Scan(&rows); err != nil {
		return err
	}
	if err := execAll(tx,
		`DROP INDEX IF EXISTS idx_outgoing_sends_due`,
		`DROP INDEX IF EXISTS idx_outgoing_sends_account`,
		`DROP INDEX IF EXISTS idx_outgoing_sends_sent_copy`,
		`ALTER TABLE outgoing_sends RENAME TO outgoing_sends_v116`,
		outgoingSendsTableSQL,
		`INSERT INTO outgoing_sends (`+columns+`) SELECT `+selected+` FROM outgoing_sends_v116`,
		`DROP TABLE outgoing_sends_v116`,
		`CREATE INDEX IF NOT EXISTS idx_outgoing_sends_due ON outgoing_sends(status, next_attempt_at, send_after)`,
		`CREATE INDEX IF NOT EXISTS idx_outgoing_sends_account ON outgoing_sends(account_id, status, send_after)`,
		`CREATE INDEX IF NOT EXISTS idx_outgoing_sends_sent_copy ON outgoing_sends(status, sent_copy_status, sent_copy_next_attempt_at)`,
	); err != nil {
		return err
	}
	var restored int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM outgoing_sends`).Scan(&restored); err != nil {
		return err
	}
	if restored != rows {
		return fmt.Errorf("rebuilding outgoing mail kept %d of %d rows", restored, rows)
	}
	for _, table := range []string{"outgoing_sends", "calendar_reply_jobs", "gofer_provider_send_receipts"} {
		if exists, err := tableExistsTx(tx, table); err != nil {
			return err
		} else if exists {
			if err := foreignKeyCheckTableTx(tx, table); err != nil {
				return err
			}
		}
	}
	return nil
}

// sharedOutgoingSendColumnsTx lists the columns the existing outgoing_sends
// has in common with the new one, and what to copy into each; older partial
// databases lack some.
func sharedOutgoingSendColumnsTx(tx *sql.Tx) (columns, selected string, err error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info('outgoing_sends')`)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", "", err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	var names, values []string
	for _, column := range strings.Split(outgoingSendColumns, ",") {
		column = strings.TrimSpace(column)
		if !present[column] {
			continue
		}
		value := column
		if fill, ok := outgoingSendFillIns[column]; ok {
			value = fill
			if column == "next_attempt_at" && !present["send_after"] {
				value = "COALESCE(next_attempt_at, CURRENT_TIMESTAMP)"
			}
		}
		names, values = append(names, column), append(values, value)
	}
	return strings.Join(names, ", "), strings.Join(values, ", "), nil
}

func foreignKeyCheckTableTx(tx *sql.Tx, table string) error {
	rows, err := tx.Query(`PRAGMA foreign_key_check(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var child, rowID, parent, key sql.NullString
		if err := rows.Scan(&child, &rowID, &parent, &key); err != nil {
			return err
		}
		return fmt.Errorf("foreign key check failed: table=%s row=%s parent=%s", child.String, rowID.String, parent.String)
	}
	return rows.Err()
}

func execAll(tx *sql.Tx, statements ...string) error {
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// outgoingSendsTableSQL matches the table in schema.sql.
const outgoingSendsTableSQL = `CREATE TABLE outgoing_sends (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	message_id INTEGER REFERENCES messages(id) ON DELETE SET NULL,
	draft_id TEXT NOT NULL DEFAULT '',
	transport TEXT NOT NULL CHECK (transport IN ('smtp', 'gmail', 'outlook', 'jmap')),
	envelope_from TEXT NOT NULL,
	envelope_recipients TEXT NOT NULL DEFAULT '[]',
	mime_data BLOB,
	message_json TEXT NOT NULL DEFAULT '',
	send_after DATETIME NOT NULL,
	next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	is_scheduled INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'pending',
	attempt_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	locked_at DATETIME,
	sent_message_id TEXT NOT NULL DEFAULT '',
	sent_copy_status TEXT NOT NULL DEFAULT 'not_required' CHECK (sent_copy_status IN ('not_required', 'pending', 'copying', 'complete', 'failed', 'ambiguous')),
	sent_copy_attempt_count INTEGER NOT NULL DEFAULT 0,
	sent_copy_last_error TEXT NOT NULL DEFAULT '',
	sent_copy_locked_at DATETIME,
	sent_copy_next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	sent_copy_uid INTEGER NOT NULL DEFAULT 0,
	sent_copy_uid_validity INTEGER NOT NULL DEFAULT 0,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(message_id)
)`

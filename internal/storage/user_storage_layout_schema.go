package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Tables that exist only in the per-user layout are versioned with SQLite's
// user_version, separately from the shared schema_version every database
// carries. Each step runs once, in order, in the transaction that records its
// version. Step 1 is the layout's original tables; it creates them if missing,
// so earlier layouts adopt the version. Later steps hold actual changes.
type layoutSchemaStep func(context.Context, *sql.Tx) error

// gofer_mailbox_credentials is not part of these steps: mailauth creates it at
// startup, and the migration's credential import requires it to be absent
// until then. Steps must not reference it.
var centralLayoutSchema = []layoutSchemaStep{
	func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, userStoreDirectorySchema+";"+accountRoutingSchema)
		return err
	},
}

var userLayoutSchema = []layoutSchemaStep{
	func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, providerSendReceiptSchema); err != nil {
			return err
		}
		return ensureUserProviderDraftSchema(ctx, tx)
	},
	allowJMAPProviderSendReceiptsTx,
	allowJMAPProviderDraftsTx,
}

// allowJMAPProviderSendReceiptsTx lets mail sent over JMAP keep a send
// receipt. The provider CHECK can only change by rebuilding the table, keeping
// its rows; a table already created with jmap is left alone.
func allowJMAPProviderSendReceiptsTx(ctx context.Context, tx *sql.Tx) error {
	var definition string
	err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='gofer_provider_send_receipts'`).Scan(&definition)
	if err == sql.ErrNoRows || (err == nil && strings.Contains(definition, "'jmap'")) {
		return nil
	}
	if err != nil {
		return err
	}
	var rows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gofer_provider_send_receipts`).Scan(&rows); err != nil {
		return err
	}
	statements := []string{
		`DROP TABLE IF EXISTS temp.gofer_provider_send_receipts_v1`,
		`CREATE TEMP TABLE gofer_provider_send_receipts_v1 AS SELECT * FROM gofer_provider_send_receipts`,
		`DROP TABLE gofer_provider_send_receipts`,
		providerSendReceiptSchema,
	}
	if rows > 0 {
		statements = append(statements, `INSERT INTO gofer_provider_send_receipts SELECT * FROM temp.gofer_provider_send_receipts_v1`)
	}
	statements = append(statements, `DROP TABLE temp.gofer_provider_send_receipts_v1`)
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	var restored int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM gofer_provider_send_receipts`).Scan(&restored); err != nil {
		return err
	}
	if restored != rows {
		return fmt.Errorf("rebuilding send receipts kept %d of %d rows", restored, rows)
	}
	return nil
}

// allowJMAPProviderDraftsTx lets drafts of JMAP accounts sync through the
// provider draft queue. The provider CHECK on the states table can only
// change by rebuilding it. Its operations reference it with ON DELETE
// CASCADE, so both are set aside first and restored with their ids; tables
// already created with jmap are left alone.
func allowJMAPProviderDraftsTx(ctx context.Context, tx *sql.Tx) error {
	var definition string
	err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='gofer_provider_draft_states'`).Scan(&definition)
	if err == sql.ErrNoRows || (err == nil && strings.Contains(definition, "'jmap'")) {
		return nil
	}
	if err != nil {
		return err
	}
	tables := []string{"gofer_provider_draft_states", "gofer_provider_draft_operations"}
	counts := map[string]int{}
	for _, table := range tables {
		var rows int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil {
			return err
		}
		counts[table] = rows
		for _, statement := range []string{
			`DROP TABLE IF EXISTS temp.` + table + `_v2`,
			`CREATE TEMP TABLE ` + table + `_v2 AS SELECT * FROM ` + table,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	// Operations first, so dropping the states cascades to nothing.
	for _, statement := range []string{`DROP TABLE gofer_provider_draft_operations`, `DROP TABLE gofer_provider_draft_states`} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := ensureUserProviderDraftSchema(ctx, tx); err != nil {
		return err
	}
	for _, table := range tables {
		if counts[table] > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+` SELECT * FROM temp.`+table+`_v2`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE temp.`+table+`_v2`); err != nil {
			return err
		}
		var restored int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&restored); err != nil {
			return err
		}
		if restored != counts[table] {
			return fmt.Errorf("rebuilding %s kept %d of %d rows", table, restored, counts[table])
		}
	}
	return nil
}

// ensureLayoutSchema only reads the version when the database is current.
func ensureLayoutSchema(ctx context.Context, db *DB, steps []layoutSchemaStep) error {
	var version int
	if err := db.Read().QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == len(steps) {
		return nil
	}
	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := applyLayoutSchemaTx(ctx, tx, steps); err != nil {
		return err
	}
	return tx.Commit()
}

func applyLayoutSchemaTx(ctx context.Context, tx *sql.Tx, steps []layoutSchemaStep) error {
	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(steps) {
		return fmt.Errorf("layout schema version %d was written by a newer Gofer release; this release supports %d", version, len(steps))
	}
	if version == len(steps) {
		return nil
	}
	for _, step := range steps[version:] {
		if err := step(ctx, tx); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, len(steps)))
	return err
}

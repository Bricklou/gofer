package storage

import (
	"context"
	"database/sql"
)

// migrateV121ToV122 adds each account's choice between being told about
// changes by its provider and checking on a schedule. It starts on, and only
// matters where Gofer can receive the provider's notifications.
func migrateV121ToV122(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 122 {
		return nil
	}
	exists, err := tableExistsTx(tx, "accounts")
	if err != nil {
		return err
	}
	if exists {
		present, err := columnExistsTx(tx, "accounts", "provider_push_enabled")
		if err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(`ALTER TABLE accounts ADD COLUMN provider_push_enabled INTEGER NOT NULL DEFAULT 1 CHECK (provider_push_enabled IN (0, 1))`); err != nil {
				return err
			}
		}
	}
	if err := markSchemaVersion(tx, 122); err != nil {
		return err
	}
	return tx.Commit()
}

// ProviderPushEnabled reports whether the account chose to be told about
// changes by its provider rather than checked on a schedule.
func (db *DB) ProviderPushEnabled(ctx context.Context, accountID string) (bool, error) {
	var enabled int
	err := db.Read().QueryRowContext(ctx, `SELECT provider_push_enabled FROM accounts WHERE id = ? AND COALESCE(is_deleting, 0) = 0`, accountID).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, ErrAccountRoute
	}
	return enabled == 1, err
}

// SetProviderPushEnabled records the owner's choice for one of their accounts.
func (db *DB) SetProviderPushEnabled(ctx context.Context, userID, accountID string, enabled bool) error {
	value := 0
	if enabled {
		value = 1
	}
	result, err := db.Write().ExecContext(ctx, `UPDATE accounts SET provider_push_enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND COALESCE(is_deleting, 0) = 0`, value, accountID, userID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return ErrAccountRoute
	}
	return nil
}

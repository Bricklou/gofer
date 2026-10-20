package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Gmail, Outlook and JMAP can hold two messages with one Message-ID, such as
// the Sent and Inbox copies of mail sent to oneself. They share a row, and
// each folder state keeps the provider id of the copy it belongs to.

// providerCopyIDSQL is the provider id of the copy a folder state belongs to.
// States written before they kept one, or by paths that don't set it yet,
// fall back to the row's id.
func providerCopyIDSQL(state string) string {
	return `COALESCE(NULLIF(` + state + `.remote_message_id, ''), (SELECT copy_row.remote_message_id FROM messages copy_row WHERE copy_row.id = ` + state + `.message_id), '')`
}

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// findProviderCopyMessage returns the row holding a copy with this provider id.
func findProviderCopyMessage(ctx context.Context, q rowQueryer, accountID, providerMessageID string) (int64, error) {
	var msgID int64
	err := q.QueryRowContext(ctx, `
		SELECT mfs.message_id FROM message_folder_state mfs
		JOIN folders f ON f.id = mfs.folder_id
		WHERE f.account_id = ? AND mfs.remote_message_id = ?
		ORDER BY mfs.is_deleted ASC LIMIT 1`, accountID, providerMessageID).Scan(&msgID)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("query provider copy id: %w", err)
	}
	if msgID != 0 {
		return msgID, nil
	}
	err = q.QueryRowContext(ctx,
		`SELECT id FROM messages WHERE account_id = ? AND remote_message_id = ? LIMIT 1`,
		accountID, providerMessageID).Scan(&msgID)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("query provider message id: %w", err)
	}
	return msgID, nil
}

// hasOtherLiveProviderCopyTx reports whether a row is also in a folder as
// another copy than providerMessageID.
func hasOtherLiveProviderCopyTx(ctx context.Context, tx *sql.Tx, messageID int64, providerMessageID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM message_folder_state
			WHERE message_id = ? AND is_deleted = 0
			  AND COALESCE(remote_message_id, '') NOT IN ('', ?)
		)`, messageID, providerMessageID).Scan(&exists)
	return exists, err
}

// refreshProviderRowIDTx points the row's provider id at a copy that is still
// in a folder, once the copy it named is gone from all of them.
func refreshProviderRowIDTx(ctx context.Context, tx *sql.Tx, messageID int64) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE messages
		SET remote_message_id = (
			SELECT mfs.remote_message_id FROM message_folder_state mfs
			WHERE mfs.message_id = messages.id AND mfs.is_deleted = 0 AND COALESCE(mfs.remote_message_id, '') != ''
			ORDER BY mfs.synced_at DESC LIMIT 1
		)
		WHERE id = ?
		  AND EXISTS (
			SELECT 1 FROM message_folder_state mfs
			WHERE mfs.message_id = messages.id AND mfs.is_deleted = 0 AND COALESCE(mfs.remote_message_id, '') != ''
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM message_folder_state mfs
			WHERE mfs.message_id = messages.id AND mfs.is_deleted = 0 AND mfs.remote_message_id = messages.remote_message_id
		  )`, messageID)
	if err != nil {
		return fmt.Errorf("refresh provider row id: %w", err)
	}
	return nil
}

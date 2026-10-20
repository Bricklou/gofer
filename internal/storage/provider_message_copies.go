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

// providerCopyOfRowSQL matches a row that holds a copy with the provider id
// bound twice after it.
func providerCopyOfRowSQL(row string) string {
	return `(COALESCE(` + row + `.remote_message_id, '') = ? OR EXISTS (
		SELECT 1 FROM message_folder_state copy_state WHERE copy_state.message_id = ` + row + `.id AND copy_state.remote_message_id = ?))`
}

// bindProviderCopyIDTx records a provider id found for the copy in a folder,
// and for the row when it has none.
func bindProviderCopyIDTx(ctx context.Context, tx *sql.Tx, messageID int64, folderID, providerMessageID string) error {
	if providerMessageID == "" {
		return nil
	}
	if folderID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE message_folder_state SET remote_message_id = ? WHERE message_id = ? AND folder_id = ? AND COALESCE(remote_message_id, '') = ''`,
			providerMessageID, messageID, folderID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE messages SET remote_message_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND COALESCE(remote_message_id, '') = ''`,
		providerMessageID, messageID)
	return err
}

// replaceProviderCopyIDTx gives a copy the new id a provider assigned it, as
// Outlook does when it moves a message. The row's id follows when it named
// that copy.
func replaceProviderCopyIDTx(ctx context.Context, tx *sql.Tx, messageID int64, previousFolderID, destinationFolderID, providerMessageID string) error {
	var previousID string
	err := tx.QueryRowContext(ctx, `SELECT `+providerCopyIDSQL("mfs")+` FROM message_folder_state mfs WHERE mfs.message_id = ? AND mfs.folder_id = ?`,
		messageID, previousFolderID).Scan(&previousID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE message_folder_state SET remote_message_id = ?
		WHERE message_id = ? AND (folder_id = ? OR (? != '' AND remote_message_id = ?))`,
		providerMessageID, messageID, destinationFolderID, previousID, previousID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE messages SET remote_message_id = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND (COALESCE(remote_message_id, '') = '' OR remote_message_id = ?)`,
		providerMessageID, messageID, previousID)
	return err
}

// providerCopyFolderScopesTx returns one folder for each copy of a message
// with its own provider id, when it has more than one. A change made to the
// message is then queued for every copy.
func providerCopyFolderScopesTx(ctx context.Context, tx *sql.Tx, messageID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT MIN(mfs.folder_id) FROM message_folder_state mfs
		WHERE mfs.message_id = ? AND mfs.is_deleted = 0 AND COALESCE(mfs.remote_message_id, '') != ''
		GROUP BY mfs.remote_message_id ORDER BY 1`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var folderIDs []string
	for rows.Next() {
		var folderID string
		if err := rows.Scan(&folderID); err != nil {
			return nil, err
		}
		folderIDs = append(folderIDs, folderID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(folderIDs) < 2 {
		return nil, nil
	}
	return folderIDs, nil
}

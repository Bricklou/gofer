package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// Gmail, Outlook and JMAP can hold two messages with one Message-ID, such as
// the Sent and Inbox copies of mail sent to oneself. They share a row, and
// each folder state keeps the provider id of the copy it belongs to. A state
// Gofer wrote before the provider named its copy, such as the Sent copy of
// mail just sent, has no id yet and belongs to no copy.

// providerCopyIDSQL is the provider id of the copy a folder state belongs to.
func providerCopyIDSQL(state string) string {
	return `COALESCE(` + state + `.remote_message_id, '')`
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

// providerCopyOfRowSQL matches a row that holds a copy with the provider id
// bound after it. An empty id matches a row none of whose copies has one.
func providerCopyOfRowSQL(row string) string {
	return `(` + anyProviderCopyIDSQL(row) + ` = '' AND ? = '' OR EXISTS (
		SELECT 1 FROM message_folder_state copy_state WHERE copy_state.message_id = ` + row + `.id AND copy_state.remote_message_id = ?))`
}

// bindProviderCopyIDTx records a provider id found for the copy in a folder,
// or, without a folder, for the row's states that have none.
func bindProviderCopyIDTx(ctx context.Context, tx *sql.Tx, messageID int64, folderID, providerMessageID string) error {
	if providerMessageID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE message_folder_state SET remote_message_id = ? WHERE message_id = ? AND (? = '' OR folder_id = ?) AND COALESCE(remote_message_id, '') = ''`,
		providerMessageID, messageID, folderID, folderID)
	return err
}

// replaceProviderCopyIDTx gives a copy the new id a provider assigned it, as
// Outlook does when it moves a message.
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
	return nil
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

// anyProviderCopyIDSQL is the provider id of one of a row's copies, one still
// in a folder when there is one. Every copy has the same content, so it is
// what content is fetched through.
func anyProviderCopyIDSQL(row string) string {
	return `COALESCE((SELECT any_copy.remote_message_id FROM message_folder_state any_copy
		WHERE any_copy.message_id = ` + row + `.id AND COALESCE(any_copy.remote_message_id, '') != ''
		ORDER BY any_copy.is_deleted ASC, any_copy.folder_id LIMIT 1), '')`
}

// stateOrAnyProviderCopyIDSQL is the id of a folder state's copy, or of
// another copy of its row while the state has none.
func stateOrAnyProviderCopyIDSQL(state, row string) string {
	return `COALESCE(NULLIF(` + state + `.remote_message_id, ''), ` + anyProviderCopyIDSQL(row) + `)`
}

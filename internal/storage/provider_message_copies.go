package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

type rowsQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// findProviderCopyMessage returns the row holding a copy with this provider id.
func findProviderCopyMessage(ctx context.Context, q rowQueryer, accountID, providerMessageID string) (int64, error) {
	var msgID int64
	err := q.QueryRowContext(ctx, `
		SELECT mfs.message_id FROM message_folder_state mfs
		JOIN folders f ON f.id = mfs.folder_id
		WHERE f.account_id = ? AND mfs.remote_message_id = ?
		ORDER BY mfs.is_deleted ASC LIMIT 1`, accountID, providerMessageID).Scan(&msgID)
	if err == sql.ErrNoRows {
		err = q.QueryRowContext(ctx, `
			SELECT c.message_id FROM message_folder_copies c
			JOIN folders f ON f.id = c.folder_id
			WHERE f.account_id = ? AND c.remote_message_id = ? LIMIT 1`, accountID, providerMessageID).Scan(&msgID)
	}
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("query provider copy id: %w", err)
	}
	return msgID, nil
}

// KnownProviderFolderCopies reports which of these provider ids already have a
// copy in the folder.
func (db *DB) KnownProviderFolderCopies(ctx context.Context, accountID, folderID string, providerMessageIDs []string) (map[string]bool, error) {
	known := map[string]bool{}
	if len(providerMessageIDs) == 0 {
		return known, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(providerMessageIDs)), ",")
	args := make([]any, 0, 2*len(providerMessageIDs)+4)
	args = append(args, folderID, accountID)
	for _, id := range providerMessageIDs {
		args = append(args, id)
	}
	args = append(args, folderID, accountID)
	for _, id := range providerMessageIDs {
		args = append(args, id)
	}
	rows, err := db.Read().QueryContext(ctx, `
		SELECT mfs.remote_message_id FROM message_folder_state mfs
		JOIN messages m ON m.id = mfs.message_id
		WHERE mfs.folder_id = ? AND mfs.is_deleted = 0 AND m.account_id = ? AND mfs.remote_message_id IN (`+marks+`)
		UNION
		SELECT c.remote_message_id FROM message_folder_copies c
		JOIN messages m ON m.id = c.message_id
		WHERE c.folder_id = ? AND m.account_id = ? AND c.remote_message_id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query known provider copies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		known[id] = true
	}
	return known, rows.Err()
}

// Two copies of a message can sit in one folder, such as both copies of mail
// sent to oneself in Trash. The folder's state belongs to one of them, and
// message_folder_copies records the others there. When the state's copy
// leaves the folder, a recorded copy takes the state over.

// recordFolderCopyTx records a copy synced into a folder whose state belongs
// to another copy still there, and reports whether it did; the state is then
// left to its copy. Otherwise the caller gives the state to this copy.
func recordFolderCopyTx(ctx context.Context, tx *sql.Tx, messageID int64, folderID, providerMessageID string) (bool, error) {
	var stateID string
	err := tx.QueryRowContext(ctx, `SELECT `+providerCopyIDSQL("mfs")+` FROM message_folder_state mfs WHERE mfs.message_id = ? AND mfs.folder_id = ? AND mfs.is_deleted = 0`,
		messageID, folderID).Scan(&stateID)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if stateID != "" && stateID != providerMessageID {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_folder_copies (message_id, folder_id, remote_message_id) VALUES (?, ?, ?)`,
			messageID, folderID, providerMessageID)
		return err == nil, err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM message_folder_copies WHERE message_id = ? AND folder_id = ? AND remote_message_id = ?`,
		messageID, folderID, providerMessageID)
	return false, err
}

// promoteFolderCopiesTx gives the live folder states matched by where, whose
// copy is leaving their folder, to another copy recorded there, so they stay.
// where is a condition on message_folder_state. Recorded copies leaving the
// same folders must be dropped first.
func promoteFolderCopiesTx(ctx context.Context, tx *sql.Tx, where string, args ...any) error {
	const recorded = `FROM message_folder_copies c
		WHERE c.message_id = message_folder_state.message_id AND c.folder_id = message_folder_state.folder_id`
	if _, err := tx.ExecContext(ctx, `
		UPDATE message_folder_state SET remote_message_id = (SELECT MIN(c.remote_message_id) `+recorded+`)
		WHERE is_deleted = 0 AND EXISTS (SELECT 1 `+recorded+`) AND (`+where+`)`, args...); err != nil {
		return fmt.Errorf("promote folder copies: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM message_folder_copies WHERE remote_message_id = (
			SELECT s.remote_message_id FROM message_folder_state s
			WHERE s.message_id = message_folder_copies.message_id AND s.folder_id = message_folder_copies.folder_id
		)`); err != nil {
		return fmt.Errorf("drop promoted folder copies: %w", err)
	}
	return nil
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
	if previousID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE message_label_copies SET remote_message_id = ? WHERE message_id = ? AND remote_message_id = ?`,
			providerMessageID, messageID, previousID); err != nil {
			return err
		}
	}
	return nil
}

// providerCopyFolderScopesTx returns one folder for each copy of a message
// with its own provider id, when it has more than one. A change made to the
// message is then queued for every copy. A folder holding further copies is
// among them, since a change made there reaches those too.
func providerCopyFolderScopesTx(ctx context.Context, tx *sql.Tx, messageID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT MIN(mfs.folder_id), 0 FROM message_folder_state mfs
		WHERE mfs.message_id = ? AND mfs.is_deleted = 0 AND COALESCE(mfs.remote_message_id, '') != ''
		GROUP BY mfs.remote_message_id
		UNION
		SELECT DISTINCT c.folder_id, 1 FROM message_folder_copies c
		JOIN message_folder_state mfs ON mfs.message_id = c.message_id AND mfs.folder_id = c.folder_id
		WHERE c.message_id = ? AND mfs.is_deleted = 0
		ORDER BY 1`, messageID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var folderIDs []string
	seen := map[string]bool{}
	copies := 0
	for rows.Next() {
		var folderID string
		var further bool
		if err := rows.Scan(&folderID, &further); err != nil {
			return nil, err
		}
		// A folder with further copies holds at least two.
		copies++
		if further {
			copies++
		}
		if !seen[folderID] {
			seen[folderID] = true
			folderIDs = append(folderIDs, folderID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if copies < 2 {
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

// Queued changes belong to a copy too. A delete removes the whole copy, so it
// belongs to every folder that copy is in. A move swaps one folder for
// another, so it belongs to its source folder, and to its destination while a
// later move continues it.

// copyFoldersTx returns the folders of the copy in a folder: that folder and,
// for a provider copy, the others with its provider id. An empty
// providerMessageID is read from the folder's state.
func copyFoldersTx(ctx context.Context, tx *sql.Tx, messageID int64, folderID, providerMessageID string) ([]string, error) {
	if providerMessageID == "" {
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(remote_message_id, '') FROM message_folder_state WHERE message_id = ? AND folder_id = ?`,
			messageID, folderID).Scan(&providerMessageID)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
	}
	folders := []string{folderID}
	if providerMessageID == "" {
		return folders, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT folder_id FROM message_folder_state WHERE message_id = ? AND remote_message_id = ? AND folder_id != ?`,
		messageID, providerMessageID, folderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			return nil, err
		}
		folders = append(folders, other)
	}
	return folders, rows.Err()
}

// folderListSQL returns placeholders and arguments for folder ids in an IN list.
func folderListSQL(folders []string) (string, []any) {
	placeholders := make([]string, len(folders))
	args := make([]any, len(folders))
	for i, folderID := range folders {
		placeholders[i] = "?"
		args[i] = folderID
	}
	return strings.Join(placeholders, ","), args
}

// deleteCopyMutationsTx removes the other changes queued for a copy once it is
// deleted. Changes queued for the whole message go too when no other copy is
// left in a folder.
func deleteCopyMutationsTx(ctx context.Context, tx *sql.Tx, messageID int64, keepID, folderID string) error {
	folders, err := copyFoldersTx(ctx, tx, messageID, folderID, "")
	if err != nil {
		return err
	}
	list, folderArgs := folderListSQL(folders)
	args := append([]any{messageID, keepID}, folderArgs...)
	args = append(args, folderArgs...)
	args = append(args, folderArgs...)
	_, err = tx.ExecContext(ctx, `
		DELETE FROM message_mutations
		WHERE message_id = ? AND id != ? AND (
			folder_id IN (`+list+`) OR destination_folder_id IN (`+list+`)
			OR (folder_id = '' AND NOT EXISTS (
				SELECT 1 FROM message_folder_state other
				WHERE other.message_id = message_mutations.message_id AND other.is_deleted = 0
				  AND other.folder_id NOT IN (`+list+`)
			))
		)`, args...)
	return err
}

// folderCopyIDs returns the further copies recorded in a message's folder.
func folderCopyIDs(ctx context.Context, q rowsQueryer, messageID int64, folderID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT remote_message_id FROM message_folder_copies WHERE message_id = ? AND folder_id = ? ORDER BY remote_message_id`, messageID, folderID)
	if err != nil {
		return nil, fmt.Errorf("query folder copies: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// dropFolderCopiesTx forgets the further copies in a folder once a delete
// made there has removed them with the state's copy.
func dropFolderCopiesTx(ctx context.Context, tx *sql.Tx, messageID int64, folderID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM message_folder_copies WHERE message_id = ? AND folder_id = ?`, messageID, folderID)
	return err
}

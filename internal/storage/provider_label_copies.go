package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// A row holding more than one provider copy shows the labels of all of them,
// and each copy can carry different ones. While a row has more than one copy,
// message_label_copies records which copies carry each of its provider labels,
// so a sync of one copy replaces only that copy's.

// liveProviderCopyIDsTx returns the provider ids of a row's copies still in a
// folder.
func liveProviderCopyIDsTx(ctx context.Context, tx *sql.Tx, messageID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT remote_message_id FROM message_folder_state
		WHERE message_id = ? AND is_deleted = 0 AND COALESCE(remote_message_id, '') != ''
		UNION
		SELECT c.remote_message_id FROM message_folder_copies c
		JOIN message_folder_state mfs ON mfs.message_id = c.message_id AND mfs.folder_id = c.folder_id
		WHERE c.message_id = ? AND mfs.is_deleted = 0
		ORDER BY 1`, messageID, messageID)
	if err != nil {
		return nil, fmt.Errorf("query live provider copies: %w", err)
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

// replaceCopyLabelsTx replaces the labels of one provider type that a sync
// found on the copy copyID of a row. With a single copy, or no copy id, they
// are the row's labels.
func (db *DB) replaceCopyLabelsTx(ctx context.Context, tx *sql.Tx, messageID int64, accountID, providerType, copyID string, labels []LabelInput) error {
	providerType = strings.TrimSpace(providerType)
	copyID = strings.TrimSpace(copyID)
	if providerType == "" {
		return nil
	}
	live, err := liveProviderCopyIDsTx(ctx, tx, messageID)
	if err != nil {
		return err
	}
	var others []string
	for _, id := range live {
		if id != copyID {
			others = append(others, id)
		}
	}
	if copyID == "" || len(others) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_label_copies WHERE message_id = ?`, messageID); err != nil {
			return fmt.Errorf("drop label copies: %w", err)
		}
		return db.replaceMessageLabelsForProviderTx(ctx, tx, messageID, accountID, providerType, labels)
	}

	const ofProvider = `SELECT id FROM labels WHERE account_id = ? AND provider_type = ?`
	// The first time a row is synced with more than one copy, Gofer cannot
	// tell which copies carry the labels it has, so it credits them to the
	// others. Their own syncs correct it.
	var recorded bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_label_copies WHERE message_id = ? AND label_id IN (`+ofProvider+`))`,
		messageID, accountID, providerType).Scan(&recorded); err != nil {
		return fmt.Errorf("check label copies: %w", err)
	}
	if !recorded {
		for _, id := range others {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO message_label_copies (message_id, label_id, remote_message_id)
				SELECT message_id, label_id, ? FROM message_labels
				WHERE message_id = ? AND label_id IN (`+ofProvider+`)`, id, messageID, accountID, providerType); err != nil {
				return fmt.Errorf("seed label copies: %w", err)
			}
		}
	}

	kept, _, err := db.resolveMessageLabelsTx(ctx, tx, accountID, withLabelProvider(labels, accountID, providerType))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_label_copies WHERE message_id = ? AND remote_message_id = ? AND label_id IN (`+ofProvider+`)`,
		messageID, copyID, accountID, providerType); err != nil {
		return fmt.Errorf("replace label copies: %w", err)
	}
	for _, label := range kept {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_label_copies (message_id, label_id, remote_message_id) VALUES (?, ?, ?)`,
			messageID, label.ID, copyID); err != nil {
			return fmt.Errorf("record label copy: %w", err)
		}
	}
	list, args := folderListSQL(append(others, copyID))
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_label_copies WHERE message_id = ? AND remote_message_id NOT IN (`+list+`)`,
		append([]any{messageID}, args...)...); err != nil {
		return fmt.Errorf("drop dead label copies: %w", err)
	}

	// The row has the labels any of its copies carries.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM message_labels
		WHERE message_id = ? AND label_id IN (`+ofProvider+`)
		  AND label_id NOT IN (SELECT label_id FROM message_label_copies WHERE message_id = ?)`,
		messageID, accountID, providerType, messageID); err != nil {
		return fmt.Errorf("drop labels no copy carries: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO message_labels (message_id, label_id)
		SELECT DISTINCT message_id, label_id FROM message_label_copies
		WHERE message_id = ? AND label_id IN (`+ofProvider+`)`, messageID, accountID, providerType); err != nil {
		return fmt.Errorf("add labels a copy carries: %w", err)
	}
	return protectPendingLabelRemovalsTx(ctx, tx, messageID, accountID, providerType)
}

// dropDeadCopyLabelsTx forgets the labels of an account's copies that are no
// longer in a folder, and takes off rows the labels no copy left carries.
// Once a row has one copy left, its labels are the row's own.
func dropDeadCopyLabelsTx(ctx context.Context, tx *sql.Tx, accountID string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT r.message_id FROM message_label_copies r
		JOIN messages m ON m.id = r.message_id
		WHERE m.account_id = ?`, accountID)
	if err != nil {
		return fmt.Errorf("query label copies: %w", err)
	}
	var messageIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		messageIDs = append(messageIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, messageID := range messageIDs {
		if err := dropDeadMessageCopyLabelsTx(ctx, tx, messageID); err != nil {
			return err
		}
	}
	return nil
}

func dropDeadMessageCopyLabelsTx(ctx context.Context, tx *sql.Tx, messageID int64) error {
	live, err := liveProviderCopyIDsTx(ctx, tx, messageID)
	if err != nil {
		return err
	}
	if len(live) > 0 {
		list, args := folderListSQL(live)
		// Labels of the provider types recorded, that no live copy carries.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM message_labels
			WHERE message_id = ? AND label_id IN (
				SELECT l.id FROM labels l
				WHERE l.provider_type IN (
					SELECT rl.provider_type FROM message_label_copies r JOIN labels rl ON rl.id = r.label_id WHERE r.message_id = ?
				) AND l.account_id IN (
					SELECT rl.account_id FROM message_label_copies r JOIN labels rl ON rl.id = r.label_id WHERE r.message_id = ?
				)
			) AND label_id NOT IN (
				SELECT label_id FROM message_label_copies WHERE message_id = ? AND remote_message_id IN (`+list+`)
			)`, append([]any{messageID, messageID, messageID, messageID}, args...)...); err != nil {
			return fmt.Errorf("drop labels of dead copies: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_label_copies WHERE message_id = ? AND remote_message_id NOT IN (`+list+`)`,
			append([]any{messageID}, args...)...); err != nil {
			return fmt.Errorf("drop dead label copies: %w", err)
		}
	}
	if len(live) < 2 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_label_copies WHERE message_id = ?`, messageID); err != nil {
			return fmt.Errorf("drop label copies: %w", err)
		}
	}
	return nil
}

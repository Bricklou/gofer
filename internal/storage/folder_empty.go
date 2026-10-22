package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ErrFolderNotEmptiable is returned for a folder other than Trash or Spam:
// only those can be emptied as a whole.
var ErrFolderNotEmptiable = errors.New("only Trash and Spam can be emptied")

// FolderEmptyResult says what EmptyFolderForUser changed.
type FolderEmptyResult struct {
	// Removed counts the messages deleted for good or moved to Trash.
	Removed int
	// Folders are the folders that changed, the Trash folders Spam went to
	// among them, for change notifications.
	Folders []FolderRef
	// NoTrash is set when an account's Spam was left as it is because the
	// account has no Trash folder to move it to.
	NoTrash bool
}

// EmptyFolderForUser empties Trash for good or moves everything in Spam to
// Trash, queueing each message's change for its provider as deleting or
// moving it alone would. A unified Trash or Spam covers that folder of every
// account it shows. Messages a provider cannot be told about (an IMAP folder
// without a remote name) are left alone, and so are accounts allow refuses,
// when allow is set.
func (db *DB) EmptyFolderForUser(ctx context.Context, folderID, userID string, allow func(accountID string) bool) (FolderEmptyResult, error) {
	var result FolderEmptyResult
	userID = strings.TrimSpace(userID)
	folderID = strings.TrimSpace(folderID)
	if userID == "" {
		return result, errors.New("message owner is required")
	}
	kind, err := db.emptyFolderKind(ctx, folderID, userID)
	if err != nil {
		return result, err
	}
	roles := []any{"trash"}
	if kind == "spam" {
		roles = []any{"spam", "junk"}
	}
	where, args, err := db.folderStateScope(ctx, folderID, userID)
	if err != nil {
		return result, err
	}
	rows, err := db.Read().QueryContext(ctx, `
		SELECT s.message_id, f.account_id, s.folder_id
		FROM message_folder_state s
		JOIN folders f ON f.id = s.folder_id
		JOIN accounts a ON a.id = f.account_id
		WHERE a.user_id = ? AND COALESCE(a.is_deleting, 0) = 0
		  AND s.is_deleted = 0 AND f.role IN (`+strings.TrimRight(strings.Repeat("?,", len(roles)), ",")+`)
		  AND (a.provider IN ('gmail', 'outlook', 'jmap') OR COALESCE(f.remote_id, '') != '')`+where+`
		ORDER BY s.folder_id, s.message_id`, append(append([]any{args[0]}, roles...), args[1:]...)...)
	if err != nil {
		return result, err
	}
	type folderMessages struct {
		folder FolderRef
		ids    []int64
	}
	var folders []*folderMessages
	byFolder := map[string]*folderMessages{}
	for rows.Next() {
		var messageID int64
		var ref FolderRef
		if err := rows.Scan(&messageID, &ref.AccountID, &ref.FolderID); err != nil {
			rows.Close()
			return result, err
		}
		group := byFolder[ref.FolderID]
		if group == nil {
			group = &folderMessages{folder: ref}
			byFolder[ref.FolderID] = group
			folders = append(folders, group)
		}
		group.ids = append(group.ids, messageID)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}

	// Accounts are checked once each, after the cursor is closed.
	allowed := map[string]bool{}
	trashOf := map[string]string{}
	for _, group := range folders {
		accountID := group.folder.AccountID
		ok, checked := allowed[accountID]
		if !checked {
			ok = allow == nil || allow(accountID)
			allowed[accountID] = ok
		}
		if !ok {
			continue
		}
		destination := ""
		if kind == "spam" {
			trash, found := trashOf[accountID]
			if !found {
				id, remoteID, err := db.GetFolderIDByRole(ctx, accountID, "trash")
				if err != nil {
					return result, err
				}
				if strings.TrimSpace(remoteID) == "" {
					id = ""
				}
				trash, trashOf[accountID] = id, id
			}
			if trash == "" {
				result.NoTrash = true
				continue
			}
			destination = trash
		}
		ids := uniquePositiveInt64s(group.ids)
		for start := 0; start < len(ids); start += folderMarkReadChunk {
			chunk, err := db.stillInFolder(ctx, ids[start:min(start+folderMarkReadChunk, len(ids))], group.folder.FolderID)
			if err != nil {
				return result, err
			}
			if destination == "" {
				err = db.permanentlyDeleteMessagesAndQueue(ctx, chunk, group.folder.FolderID, userID)
			} else {
				err = db.moveMessagesAndQueue(ctx, chunk, group.folder.FolderID, destination, userID)
			}
			if err != nil {
				return result, err
			}
			result.Removed += len(chunk)
		}
		result.Folders = append(result.Folders, group.folder)
		if destination != "" {
			result.Folders = append(result.Folders, FolderRef{AccountID: accountID, FolderID: destination})
		}
	}
	return result, nil
}

// emptyFolderKind says whether folderID is a Trash ("trash") or a Spam
// ("spam") folder of the user, unified or one account's.
func (db *DB) emptyFolderKind(ctx context.Context, folderID, userID string) (string, error) {
	switch folderID {
	case "trash", "spam":
		return folderID, nil
	case "":
		return "", ErrFolderNotEmptiable
	}
	if isUnifiedFolderID(folderID) || isStarredFolder(folderID) {
		return "", ErrFolderNotEmptiable
	}
	var role string
	err := db.Read().QueryRowContext(ctx, `
		SELECT COALESCE(f.role, '') FROM folders f JOIN accounts a ON a.id = f.account_id
		WHERE f.id = ? AND a.user_id = ? AND COALESCE(a.is_deleting, 0) = 0`, folderID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", sql.ErrNoRows
	}
	if err != nil {
		return "", err
	}
	switch role {
	case "trash":
		return "trash", nil
	case "spam", "junk":
		return "spam", nil
	}
	return "", ErrFolderNotEmptiable
}

// stillInFolder keeps the messages a folder still shows. Each delete or move
// insists on its message being there, and sync may have taken some away
// since they were listed.
func (db *DB) stillInFolder(ctx context.Context, ids []int64, folderID string) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{folderID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.Read().QueryContext(ctx, `SELECT message_id FROM message_folder_state
		WHERE folder_id = ? AND is_deleted = 0 AND message_id IN (`+strings.TrimRight(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kept []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		kept = append(kept, id)
	}
	return kept, rows.Err()
}

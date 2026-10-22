package storage

import (
	"context"
	"errors"
	"strings"
)

// ErrFolderNotMarkable is returned for folders "mark all as read" does not
// cover: Starred and Scheduled are views across folders, not folders.
var ErrFolderNotMarkable = errors.New("folder cannot be marked as read")

// folderMarkReadChunk is how many messages one transaction marks, so a large
// folder does not hold the write lock for long.
const folderMarkReadChunk = 500

// FolderMarkReadResult says what MarkFolderReadForUser changed.
type FolderMarkReadResult struct {
	// Marked counts the messages that were unread.
	Marked int
	// Folders are the folders whose unread messages were marked, with their
	// accounts, for change notifications.
	Folders []FolderRef
}

// FolderRef names a folder and the account it belongs to.
type FolderRef struct {
	AccountID string
	FolderID  string
}

// MarkFolderReadForUser marks every unread message in a folder as read, and
// queues the change for the provider like marking each message would. A
// unified folder such as "inbox" covers that folder of every account it
// shows. Messages a provider cannot be told about (an IMAP folder without a
// remote name) are left alone, and so are accounts allow refuses, when allow
// is set.
func (db *DB) MarkFolderReadForUser(ctx context.Context, folderID, userID string, allow func(accountID string) bool) (FolderMarkReadResult, error) {
	var result FolderMarkReadResult
	userID = strings.TrimSpace(userID)
	folderID = strings.TrimSpace(folderID)
	if userID == "" {
		return result, errors.New("message owner is required")
	}
	if folderID == "" || folderID == "scheduled" || isStarredFolder(folderID) {
		return result, ErrFolderNotMarkable
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
		  AND s.is_deleted = 0 AND s.is_read = 0
		  AND (a.provider IN ('gmail', 'outlook', 'jmap') OR COALESCE(f.remote_id, '') != '')`+where+`
		ORDER BY s.message_id`, args...)
	if err != nil {
		return result, err
	}
	type candidate struct {
		messageID int64
		folder    FolderRef
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.messageID, &c.folder.AccountID, &c.folder.FolderID); err != nil {
			rows.Close()
			return result, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}

	// Accounts are checked once each, after the cursor is closed.
	allowed := make(map[string]bool)
	seenFolders := make(map[FolderRef]bool)
	var messageIDs []int64
	for _, c := range candidates {
		ok, checked := allowed[c.folder.AccountID]
		if !checked {
			ok = allow == nil || allow(c.folder.AccountID)
			allowed[c.folder.AccountID] = ok
		}
		if !ok {
			continue
		}
		messageIDs = append(messageIDs, c.messageID)
		if !seenFolders[c.folder] {
			seenFolders[c.folder] = true
			result.Folders = append(result.Folders, c.folder)
		}
	}
	messageIDs = uniquePositiveInt64s(messageIDs)

	for start := 0; start < len(messageIDs); start += folderMarkReadChunk {
		chunk := messageIDs[start:min(start+folderMarkReadChunk, len(messageIDs))]
		if err := db.setMessageStateAndQueue(ctx, chunk, MessageMutationRead, true, userID); err != nil {
			return result, err
		}
		result.Marked += len(chunk)
	}
	return result, nil
}

// folderStateScope gives the SQL that narrows message_folder_state s, joined
// to its folder f and account a, to one folder of the user, or for a unified
// folder such as "inbox" to that folder of every account it shows. Its
// arguments start with the user, for the a.user_id test the caller writes
// before it.
func (db *DB) folderStateScope(ctx context.Context, folderID, userID string) (string, []any, error) {
	if !isUnifiedFolderID(folderID) {
		return ` AND s.folder_id = ?`, []any{userID, folderID}, nil
	}
	rolePredicate, roleArgs := unifiedFolderRolePredicate("f", folderID)
	accountFilter, accountArgs, err := db.unifiedFolderAccountFilter(ctx, userID, folderID, "a")
	if err != nil {
		return "", nil, err
	}
	return ` AND ` + rolePredicate + accountFilter, append(append([]any{userID}, roleArgs...), accountArgs...), nil
}

package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	mailmessage "github.com/cristianadrielbraun/gofer/internal/mail/message"
)

// MailExportFolder is a folder an account's mail can be exported from. Path
// is its name under its parent folders, separated by "/".
type MailExportFolder struct {
	ID       string
	Name     string
	Path     string
	Role     string
	RemoteID string
	// ProviderRemoteID is the Gmail label or Outlook folder behind the folder.
	ProviderRemoteID string
	Messages         int
}

// MailExportMessage is one message in a folder being exported, with the
// state written next to it and what is needed to download it.
type MailExportMessage struct {
	ID        int64
	RawPath   string
	FromEmail string
	Date      time.Time
	RemoteUID uint32
	Read      bool
	Flagged   bool
	Draft     bool
}

// MailExportFolders lists an account's folders with the number of messages
// Gofer has in each. Starred is left out: it shows messages that live in
// other folders.
func (db *DB) MailExportFolders(ctx context.Context, accountID string) ([]MailExportFolder, error) {
	rows, err := db.Read().QueryContext(ctx, `
		SELECT f.id, COALESCE(f.parent_id, ''), f.name, f.role, COALESCE(f.remote_id, ''), f.provider_remote_id,
		       (SELECT COUNT(*) FROM message_folder_state s WHERE s.folder_id = f.id AND s.is_deleted = 0)
		FROM folders f
		WHERE f.account_id = ? AND f.selectable = 1 AND f.role != 'starred'
		  AND COALESCE(f.discovery_state, 'active') = 'active'
		ORDER BY f.sort_order, f.name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type folderRow struct {
		folder MailExportFolder
		parent string
	}
	var listed []folderRow
	names := map[string]folderRow{}
	for rows.Next() {
		var row folderRow
		if err := rows.Scan(&row.folder.ID, &row.parent, &row.folder.Name, &row.folder.Role, &row.folder.RemoteID, &row.folder.ProviderRemoteID, &row.folder.Messages); err != nil {
			return nil, err
		}
		listed = append(listed, row)
		names[row.folder.ID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Parents that can't hold mail still name the folders under them.
	parents, err := db.Read().QueryContext(ctx, `SELECT id, COALESCE(parent_id, ''), name FROM folders WHERE account_id = ?`, accountID)
	if err != nil {
		return nil, err
	}
	defer parents.Close()
	for parents.Next() {
		var row folderRow
		if err := parents.Scan(&row.folder.ID, &row.parent, &row.folder.Name); err != nil {
			return nil, err
		}
		if _, found := names[row.folder.ID]; !found {
			names[row.folder.ID] = row
		}
	}
	if err := parents.Err(); err != nil {
		return nil, err
	}
	folders := make([]MailExportFolder, 0, len(listed))
	for _, row := range listed {
		path := []string{row.folder.Name}
		seen := map[string]bool{row.folder.ID: true}
		for parent := row.parent; parent != "" && !seen[parent]; {
			seen[parent] = true
			above, found := names[parent]
			if !found {
				break
			}
			// Some servers name a folder by its full path already.
			for _, separator := range []string{"/", "."} {
				if prefix := above.folder.Name + separator; strings.HasPrefix(path[0], prefix) {
					path[0] = strings.TrimPrefix(path[0], prefix)
					break
				}
			}
			path = append([]string{above.folder.Name}, path...)
			parent = above.parent
		}
		row.folder.Path = strings.Join(path, "/")
		folders = append(folders, row.folder)
	}
	return folders, nil
}

// MailExportMessages returns up to limit messages of a folder after the
// message afterID, in the order an export writes them.
func (db *DB) MailExportMessages(ctx context.Context, accountID, folderID string, afterID int64, limit int) ([]MailExportMessage, error) {
	rows, err := db.Read().QueryContext(ctx, `
		SELECT m.id, COALESCE(m.raw_path, ''), m.from_email, CAST(COALESCE(m.date_received, m.date_sent, '') AS TEXT),
		       COALESCE(s.remote_uid, 0), s.is_read, MAX(s.is_flagged, s.is_starred), s.is_draft
		FROM message_folder_state s
		JOIN messages m ON m.id = s.message_id
		JOIN folders f ON f.id = s.folder_id
		WHERE s.folder_id = ? AND f.account_id = ? AND m.account_id = ? AND s.is_deleted = 0 AND m.id > ?
		ORDER BY m.id
		LIMIT ?`, folderID, accountID, accountID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMailExportMessages(rows)
}

func scanMailExportMessages(rows *sql.Rows) ([]MailExportMessage, error) {
	var messages []MailExportMessage
	for rows.Next() {
		var message MailExportMessage
		var date string
		var uid int64
		if err := rows.Scan(&message.ID, &message.RawPath, &message.FromEmail, &date, &uid, &message.Read, &message.Flagged, &message.Draft); err != nil {
			return nil, err
		}
		if parsed, ok := parseSQLiteDateTime(date); ok {
			message.Date = parsed
		}
		if uid > 0 && uid <= int64(^uint32(0)) {
			message.RemoteUID = uint32(uid)
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// FolderMessageIDs returns the Message-IDs of the messages Gofer has in a
// folder, as message.NormalizeMessageID writes them.
func (db *DB) FolderMessageIDs(ctx context.Context, accountID, folderID string) (map[string]bool, error) {
	rows, err := db.Read().QueryContext(ctx, `
		SELECT COALESCE(m.message_id_normalized, ''), COALESCE(m.internet_message_id, '')
		FROM message_folder_state s
		JOIN messages m ON m.id = s.message_id
		WHERE s.folder_id = ? AND m.account_id = ? AND s.is_deleted = 0`, folderID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var normalized, raw string
		if err := rows.Scan(&normalized, &raw); err != nil {
			return nil, err
		}
		if normalized == "" {
			normalized = mailmessage.NormalizeMessageID(raw)
		}
		if normalized != "" {
			ids[normalized] = true
		}
	}
	return ids, rows.Err()
}

// MailExportMessagesByID returns the messages of a folder among ids, in ID
// order. Messages no longer in the folder are left out.
func (db *DB) MailExportMessagesByID(ctx context.Context, accountID, folderID string, ids []int64) ([]MailExportMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{folderID, accountID, accountID}
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := db.Read().QueryContext(ctx, `
		SELECT m.id, COALESCE(m.raw_path, ''), m.from_email, CAST(COALESCE(m.date_received, m.date_sent, '') AS TEXT),
		       COALESCE(s.remote_uid, 0), s.is_read, MAX(s.is_flagged, s.is_starred), s.is_draft
		FROM message_folder_state s
		JOIN messages m ON m.id = s.message_id
		JOIN folders f ON f.id = s.folder_id
		WHERE s.folder_id = ? AND f.account_id = ? AND m.account_id = ? AND s.is_deleted = 0
		  AND m.id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY m.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMailExportMessages(rows)
}

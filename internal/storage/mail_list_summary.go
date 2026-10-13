package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

// summaryListScope answers a mail list from folder_thread_state when every
// active filter and the sort order only need what that table already keeps
// per conversation: read, starred and attachment state, size, account and the
// head message. Such lists then cost the same as the unfiltered list instead
// of grouping every message in the folder. It reports false for anything
// else (search, sender, dates, tags, the Starred and Scheduled views), which
// keep the general query.
//
// The returned scope is the joins and WHERE clause after
// "FROM folder_thread_state fts JOIN messages m ... JOIN accounts a ...".
func (db *DB) summaryListScope(ctx context.Context, userID, folderID string, filters models.EmailFilters) (string, []any, bool, error) {
	if !summaryFiltersOnly(filters) || isStarredFolder(folderID) || folderID == "scheduled" {
		return "", nil, false, nil
	}
	var where string
	var args []any
	if isUnifiedFolderID(folderID) {
		if userID == "" {
			return "", nil, false, nil
		}
		if err := db.ensureFolderThreadStateForUserRole(ctx, userID, folderID); err != nil {
			return "", nil, false, err
		}
		rolePredicate, roleArgs := unifiedFolderRolePredicate("f", folderID)
		accountFilter, accountArgs, err := db.unifiedFolderAccountFilter(ctx, userID, folderID, "owner")
		if err != nil {
			return "", nil, false, err
		}
		where = `JOIN folders f ON fts.folder_id = f.id
			JOIN accounts owner ON f.account_id = owner.id
			WHERE owner.user_id = ? AND ` + rolePredicate + accountFilter
		args = append([]any{userID}, roleArgs...)
		args = append(args, accountArgs...)
	} else {
		if err := db.ensureFolderThreadState(ctx, folderID); err != nil {
			return "", nil, false, err
		}
		where = `WHERE fts.folder_id = ?`
		args = []any{folderID}
	}
	if filters.Unread {
		where += ` AND fts.thread_is_read = 0`
	} else if filters.Read {
		where += ` AND fts.thread_is_read = 1`
	}
	if filters.Starred {
		where += ` AND fts.thread_is_starred = 1`
	}
	if filters.Attachments {
		where += ` AND fts.thread_has_attachments = 1`
	} else if filters.NoAttach {
		where += ` AND fts.thread_has_attachments = 0`
	}
	if filters.ThreadsOnly {
		where += ` AND fts.thread_count > 1`
	} else if filters.NoThreads {
		where += ` AND fts.thread_count = 1`
	}
	if filters.AccountID != "" {
		where += ` AND fts.account_id = ?`
		args = append(args, filters.AccountID)
	}
	// A search keeps the conversations with a matching message anywhere,
	// as the full list query does. A folder belongs to one account, so its
	// list compares conversation keys alone, which SQLite looks up directly;
	// a unified list compares account and key. The alias is not m, so the
	// page is still chosen from the summary index before joining messages.
	if match := searchMatchQuery(filters); match != "" {
		// CROSS JOIN keeps the search first: otherwise SQLite may walk the
		// account's messages and search once per message.
		hits := `FROM message_search CROSS JOIN messages hit ON hit.id = message_search.rowid WHERE message_search MATCH ?`
		key := `COALESCE(NULLIF(hit.thread_id, ''), printf('msg:%d', hit.id))`
		if isUnifiedFolderID(folderID) {
			where += ` AND (fts.account_id, fts.thread_key) IN (SELECT hit.account_id, ` + key + ` ` + hits + `)`
			args = append(args, match)
		} else {
			where += ` AND fts.thread_key IN (SELECT ` + key + ` ` + hits + `
				AND hit.account_id = (SELECT account_id FROM folders WHERE id = ?))`
			args = append(args, match, folderID)
		}
	}
	return where, args, true, nil
}

// summaryFiltersOnly reports whether the filters use nothing beyond the
// per-conversation summary, a full-text search and a supported sort.
func summaryFiltersOnly(filters models.EmailFilters) bool {
	flags := filters
	flags.Unread, flags.Read, flags.Starred = false, false, false
	flags.Attachments, flags.NoAttach = false, false
	flags.ThreadsOnly, flags.NoThreads = false, false
	flags.AccountID = ""
	flags.Query, flags.Body = "", ""
	if !emailFiltersEmpty(flags) {
		return false
	}
	switch filters.SortBy {
	case "", "date", "sender", "subject":
		return true
	}
	return false
}

// summaryOrderSQL is emailOrderSQL for lists read from folder_thread_state,
// where the conversation's newest message is m.
func summaryOrderSQL(filters models.EmailFilters) string {
	direction := "DESC"
	if filters.SortOrder == "asc" {
		direction = "ASC"
	}
	switch filters.SortBy {
	case "sender":
		return "LOWER(COALESCE(NULLIF(m.from_name, ''), m.from_email, '')) " + direction + ", m.date_received DESC, m.id DESC"
	case "subject":
		return "LOWER(COALESCE(m.subject, '')) " + direction + ", m.date_received DESC, m.id DESC"
	default:
		return "fts.last_message_at " + direction + ", fts.head_message_id " + direction
	}
}

func (db *DB) countSummaryList(ctx context.Context, where string, args []any) (int, error) {
	var count int
	err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM folder_thread_state fts `+where, args...).Scan(&count)
	return count, err
}

// summaryEmailPage loads one page of a summary-backed list. A negative
// knownTotal is counted here.
func (db *DB) summaryEmailPage(ctx context.Context, where string, args []any, filters models.EmailFilters, start, limit, knownTotal int) (*models.EmailPage, error) {
	total := knownTotal
	if total < 0 {
		var err error
		if total, err = db.countSummaryList(ctx, where, args); err != nil {
			return nil, err
		}
	}
	if start >= total {
		return &models.EmailPage{TotalCount: total, DisplayTotalCount: total, WindowStart: start, WindowEnd: start}, nil
	}
	emails, err := db.listEmailsFromFolderThreadStateOrdered(ctx, where, args, summaryOrderSQL(filters), start, limit)
	if err != nil {
		return nil, err
	}
	end := start + len(emails)
	page := &models.EmailPage{
		Emails:            emails,
		TotalCount:        total,
		DisplayTotalCount: total,
		WindowStart:       start,
		WindowEnd:         end - 1,
		HasMore:           len(emails) > 0 && end < total,
	}
	if page.HasMore {
		page.NextCursor = emails[len(emails)-1].ID
	}
	return page, nil
}

// summaryPosition returns how many rows come before the conversation of
// messageID in a summary-backed list in its default order, by counting index
// entries rather than grouping the folder's messages. A message the list does
// not show is reported at position 0.
func (db *DB) summaryPosition(ctx context.Context, where string, args []any, messageID int64) (int, error) {
	var last sql.NullString
	var head int64
	err := db.Read().QueryRowContext(ctx, `SELECT CAST(fts.last_message_at AS TEXT), fts.head_message_id
		FROM folder_thread_state fts `+where+`
		  AND fts.thread_key = (SELECT COALESCE(NULLIF(thread_id, ''), printf('msg:%d', id)) FROM messages WHERE id = ?)
		ORDER BY fts.last_message_at DESC, fts.head_message_id DESC
		LIMIT 1`, append(append([]any{}, args...), messageID)...).Scan(&last, &head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// Rows before it in "last_message_at DESC, head_message_id DESC" order,
	// where a missing date sorts last.
	before := ` AND (fts.last_message_at, fts.head_message_id) > (?, ?)`
	beforeArgs := []any{last.String, head}
	if !last.Valid {
		before = ` AND (fts.last_message_at IS NOT NULL OR fts.head_message_id > ?)`
		beforeArgs = []any{head}
	}
	return db.countSummaryList(ctx, where+before, append(append([]any{}, args...), beforeArgs...))
}

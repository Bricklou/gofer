package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// seedFolderMarkReadTest makes an IMAP and a Gmail account for the default
// user and an IMAP account for someone else, each with an Inbox and an
// Archive, and puts unread mail in them: inboxUnread messages in the IMAP
// Inbox, two in the Gmail Inbox, one in the IMAP Archive and one in the other
// user's Inbox.
func seedFolderMarkReadTest(t *testing.T, inboxUnread int) *DB {
	t.Helper()
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO users (id, username, username_normalized, name) VALUES ('someone-else', 'someone-else', 'someone-else', 'Someone Else')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	for _, account := range []struct{ id, user, provider string }{
		{"imap-acc", "default", "imap"},
		{"gmail-acc", "default", MessageMutationProviderGmail},
		{"other-acc", "someone-else", "imap"},
	} {
		if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, provider, email_address) VALUES (?, ?, ?, ?)`, account.id, account.user, account.provider, account.id+"@example.com"); err != nil {
			t.Fatalf("insert account %s: %v", account.id, err)
		}
		if err := db.UpsertFolders(ctx, []UpsertFolderInput{
			{ID: account.id + "-inbox", AccountID: account.id, RemoteID: "INBOX", Name: "Inbox", Role: "inbox", Selectable: true},
			{ID: account.id + "-archive", AccountID: account.id, RemoteID: "Archive", Name: "Archive", Role: "archive", Selectable: true},
		}); err != nil {
			t.Fatalf("UpsertFolders(%s) error = %v", account.id, err)
		}
	}
	seed := func(accountID, folderID string, count int, read bool) {
		t.Helper()
		messages := make([]SyncMessage, 0, count)
		for i := range count {
			messages = append(messages, SyncMessage{
				AccountID: accountID, FolderID: folderID, RemoteUID: uint32(i + 1),
				MessageID: fmt.Sprintf("<%s-%d@example.com>", folderID, i), Subject: "Garden club notes",
				FromEmail: "mira@example.com", DateSent: time.Now(), IsRead: read,
			})
		}
		if err := db.UpsertSyncMessages(ctx, messages); err != nil {
			t.Fatalf("UpsertSyncMessages(%s) error = %v", folderID, err)
		}
	}
	seed("imap-acc", "imap-acc-inbox", inboxUnread, false)
	seed("gmail-acc", "gmail-acc-inbox", 2, false)
	seed("imap-acc", "imap-acc-archive", 1, false)
	seed("other-acc", "other-acc-inbox", 1, false)
	return db
}

func unreadInFolder(t *testing.T, db *DB, folderID string) int {
	t.Helper()
	var count int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM message_folder_state WHERE folder_id = ? AND is_deleted = 0 AND is_read = 0`, folderID).Scan(&count); err != nil {
		t.Fatalf("count unread in %s: %v", folderID, err)
	}
	return count
}

func TestMarkFolderReadMarksAndQueuesEveryUnreadMessage(t *testing.T) {
	// More than one transaction's worth.
	const unread = folderMarkReadChunk + 20
	db := seedFolderMarkReadTest(t, unread)
	ctx := context.Background()

	result, err := db.MarkFolderReadForUser(ctx, "imap-acc-inbox", "default", nil)
	if err != nil {
		t.Fatalf("MarkFolderReadForUser() error = %v", err)
	}
	if result.Marked != unread {
		t.Fatalf("Marked = %d, want %d", result.Marked, unread)
	}
	if len(result.Folders) != 1 || result.Folders[0] != (FolderRef{AccountID: "imap-acc", FolderID: "imap-acc-inbox"}) {
		t.Fatalf("Folders = %#v", result.Folders)
	}
	if got := unreadInFolder(t, db, "imap-acc-inbox"); got != 0 {
		t.Fatalf("unread left in the folder = %d, want 0", got)
	}
	if got := unreadInFolder(t, db, "imap-acc-archive"); got != 1 {
		t.Fatalf("unread in another folder = %d, want it untouched at 1", got)
	}
	var queued int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM message_mutations WHERE kind = ? AND target_value = 1 AND folder_id = 'imap-acc-inbox' AND status = ?`, MessageMutationRead, MessageMutationPending).Scan(&queued); err != nil {
		t.Fatalf("count queued mutations: %v", err)
	}
	if queued != unread {
		t.Fatalf("queued read mutations = %d, want one per message (%d)", queued, unread)
	}
	var stored int
	if err := db.Read().QueryRow(`SELECT unread_count FROM folders WHERE id = 'imap-acc-inbox'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("stored unread count = %d, %v; want 0", stored, err)
	}

	again, err := db.MarkFolderReadForUser(ctx, "imap-acc-inbox", "default", nil)
	if err != nil || again.Marked != 0 || len(again.Folders) != 0 {
		t.Fatalf("second MarkFolderReadForUser() = %#v, %v; want nothing left to mark", again, err)
	}
}

func TestMarkFolderReadCoversAUnifiedFolderOfTheUsersAccountsOnly(t *testing.T) {
	db := seedFolderMarkReadTest(t, 3)
	result, err := db.MarkFolderReadForUser(context.Background(), "inbox", "default", nil)
	if err != nil {
		t.Fatalf("MarkFolderReadForUser(inbox) error = %v", err)
	}
	if result.Marked != 5 || len(result.Folders) != 2 {
		t.Fatalf("result = %#v, want 5 messages in 2 folders", result)
	}
	for _, folderID := range []string{"imap-acc-inbox", "gmail-acc-inbox"} {
		if got := unreadInFolder(t, db, folderID); got != 0 {
			t.Fatalf("unread in %s = %d, want 0", folderID, got)
		}
	}
	if got := unreadInFolder(t, db, "other-acc-inbox"); got != 1 {
		t.Fatalf("another user's unread = %d, want it untouched at 1", got)
	}
	if got := unreadInFolder(t, db, "imap-acc-archive"); got != 1 {
		t.Fatalf("Archive unread = %d, want it untouched at 1", got)
	}
}

func TestMarkFolderReadLeavesRefusedAccountsAlone(t *testing.T) {
	db := seedFolderMarkReadTest(t, 3)
	var asked []string
	result, err := db.MarkFolderReadForUser(context.Background(), "inbox", "default", func(accountID string) bool {
		asked = append(asked, accountID)
		return accountID != "gmail-acc"
	})
	if err != nil {
		t.Fatalf("MarkFolderReadForUser() error = %v", err)
	}
	if result.Marked != 3 || len(result.Folders) != 1 || result.Folders[0].AccountID != "imap-acc" {
		t.Fatalf("result = %#v, want only the IMAP account's 3 messages", result)
	}
	if got := unreadInFolder(t, db, "gmail-acc-inbox"); got != 2 {
		t.Fatalf("refused account's unread = %d, want 2", got)
	}
	if len(asked) != 2 {
		t.Fatalf("allow asked about %v, want each account once", asked)
	}
}

func TestMarkFolderReadRefusesViewsAndOtherUsersFolders(t *testing.T) {
	db := seedFolderMarkReadTest(t, 1)
	ctx := context.Background()
	for _, folderID := range []string{"starred", "starred-imap-acc", "scheduled", ""} {
		if _, err := db.MarkFolderReadForUser(ctx, folderID, "default", nil); !errors.Is(err, ErrFolderNotMarkable) {
			t.Fatalf("MarkFolderReadForUser(%q) error = %v, want ErrFolderNotMarkable", folderID, err)
		}
	}
	result, err := db.MarkFolderReadForUser(ctx, "other-acc-inbox", "default", nil)
	if err != nil || result.Marked != 0 {
		t.Fatalf("MarkFolderReadForUser(another user's folder) = %#v, %v; want nothing marked", result, err)
	}
	if got := unreadInFolder(t, db, "other-acc-inbox"); got != 1 {
		t.Fatalf("another user's unread = %d, want 1", got)
	}
}

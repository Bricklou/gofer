package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// seedFolderEmptyTest makes two IMAP accounts for the default user, one
// without a Trash folder, and one for someone else. Each account has an
// Inbox and Spam (and Trash where it has one) with mail in each folder:
// trashCount messages in the first account's Trash, two in each Spam, one in
// each Inbox and one in the other user's Trash.
func seedFolderEmptyTest(t *testing.T, trashCount int) *DB {
	t.Helper()
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO users (id, username, username_normalized, name) VALUES ('someone-else', 'someone-else', 'someone-else', 'Someone Else')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	for _, account := range []struct {
		id, user string
		trash    bool
	}{
		{"main-acc", "default", true},
		{"bare-acc", "default", false},
		{"other-acc", "someone-else", true},
	} {
		if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, provider, email_address) VALUES (?, ?, 'imap', ?)`, account.id, account.user, account.id+"@example.com"); err != nil {
			t.Fatalf("insert account %s: %v", account.id, err)
		}
		folders := []UpsertFolderInput{
			{ID: account.id + "-inbox", AccountID: account.id, RemoteID: "INBOX", Name: "Inbox", Role: "inbox", Selectable: true},
			{ID: account.id + "-spam", AccountID: account.id, RemoteID: "Junk", Name: "Junk", Role: "junk", Selectable: true},
		}
		if account.trash {
			folders = append(folders, UpsertFolderInput{ID: account.id + "-trash", AccountID: account.id, RemoteID: "Trash", Name: "Trash", Role: "trash", Selectable: true})
		}
		if err := db.UpsertFolders(ctx, folders); err != nil {
			t.Fatalf("UpsertFolders(%s) error = %v", account.id, err)
		}
	}
	seed := func(accountID, folderID string, count int) {
		t.Helper()
		messages := make([]SyncMessage, 0, count)
		for i := range count {
			messages = append(messages, SyncMessage{
				AccountID: accountID, FolderID: folderID, RemoteUID: uint32(i + 1),
				MessageID: fmt.Sprintf("<%s-%d@example.com>", folderID, i), Subject: "Pottery sale",
				FromEmail: "dara@example.com", DateSent: time.Now(),
			})
		}
		if err := db.UpsertSyncMessages(ctx, messages); err != nil {
			t.Fatalf("UpsertSyncMessages(%s) error = %v", folderID, err)
		}
	}
	seed("main-acc", "main-acc-trash", trashCount)
	seed("main-acc", "main-acc-spam", 2)
	seed("main-acc", "main-acc-inbox", 1)
	seed("bare-acc", "bare-acc-spam", 2)
	seed("bare-acc", "bare-acc-inbox", 1)
	seed("other-acc", "other-acc-trash", 1)
	return db
}

func shownInFolder(t *testing.T, db *DB, folderID string) int {
	t.Helper()
	var count int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM message_folder_state WHERE folder_id = ? AND is_deleted = 0`, folderID).Scan(&count); err != nil {
		t.Fatalf("count messages in %s: %v", folderID, err)
	}
	return count
}

func queuedMutations(t *testing.T, db *DB, kind, folderID string) int {
	t.Helper()
	var count int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM message_mutations WHERE kind = ? AND folder_id = ?`, kind, folderID).Scan(&count); err != nil {
		t.Fatalf("count %s mutations: %v", kind, err)
	}
	return count
}

func TestEmptyFolderDeletesAllOfTrashForGood(t *testing.T) {
	// More than one transaction's worth.
	const trash = folderMarkReadChunk + 20
	db := seedFolderEmptyTest(t, trash)
	result, err := db.EmptyFolderForUser(context.Background(), "main-acc-trash", "default", nil)
	if err != nil {
		t.Fatalf("EmptyFolderForUser() error = %v", err)
	}
	if result.Removed != trash || len(result.Folders) != 1 || result.NoTrash {
		t.Fatalf("result = %#v, want %d removed from one folder", result, trash)
	}
	if got := shownInFolder(t, db, "main-acc-trash"); got != 0 {
		t.Fatalf("Trash still shows %d messages", got)
	}
	if got := queuedMutations(t, db, MessageMutationDelete, "main-acc-trash"); got != trash {
		t.Fatalf("queued deletes = %d, want one per message (%d)", got, trash)
	}
	if shownInFolder(t, db, "main-acc-inbox") != 1 || shownInFolder(t, db, "main-acc-spam") != 2 || shownInFolder(t, db, "other-acc-trash") != 1 {
		t.Fatal("emptying one Trash touched other folders or another user's mail")
	}
}

func TestEmptyFolderMovesSpamToEachAccountsTrash(t *testing.T) {
	db := seedFolderEmptyTest(t, 1)
	result, err := db.EmptyFolderForUser(context.Background(), "spam", "default", nil)
	if err != nil {
		t.Fatalf("EmptyFolderForUser(spam) error = %v", err)
	}
	if result.Removed != 2 || !result.NoTrash {
		t.Fatalf("result = %#v, want the main account's 2 moved and the bare account's left for want of a Trash", result)
	}
	if shownInFolder(t, db, "main-acc-spam") != 0 || shownInFolder(t, db, "main-acc-trash") != 3 {
		t.Fatalf("main account Spam = %d, Trash = %d; want 0 and 3", shownInFolder(t, db, "main-acc-spam"), shownInFolder(t, db, "main-acc-trash"))
	}
	if got := shownInFolder(t, db, "bare-acc-spam"); got != 2 {
		t.Fatalf("an account without Trash lost %d spam messages", 2-got)
	}
	if got := queuedMutations(t, db, MessageMutationMove, "main-acc-spam"); got != 2 {
		t.Fatalf("queued moves = %d, want 2", got)
	}
	want := map[FolderRef]bool{{AccountID: "main-acc", FolderID: "main-acc-spam"}: true, {AccountID: "main-acc", FolderID: "main-acc-trash"}: true}
	if len(result.Folders) != len(want) || !want[result.Folders[0]] || !want[result.Folders[1]] {
		t.Fatalf("changed folders = %#v, want Spam and the Trash it went to", result.Folders)
	}
}

func TestEmptyFolderCoversAUnifiedTrashOfTheUsersAccountsOnly(t *testing.T) {
	db := seedFolderEmptyTest(t, 3)
	var asked []string
	result, err := db.EmptyFolderForUser(context.Background(), "trash", "default", func(accountID string) bool {
		asked = append(asked, accountID)
		return true
	})
	if err != nil || result.Removed != 3 {
		t.Fatalf("EmptyFolderForUser(trash) = %#v, %v; want 3 removed", result, err)
	}
	if got := shownInFolder(t, db, "other-acc-trash"); got != 1 {
		t.Fatalf("another user's Trash = %d, want it untouched at 1", got)
	}
	if len(asked) != 1 || asked[0] != "main-acc" {
		t.Fatalf("allow asked about %v, want the account with Trash mail once", asked)
	}

	refused, err := db.EmptyFolderForUser(context.Background(), "main-acc-spam", "default", func(string) bool { return false })
	if err != nil || refused.Removed != 0 || shownInFolder(t, db, "main-acc-spam") != 2 {
		t.Fatalf("refused account result = %#v, %v; want its Spam left alone", refused, err)
	}
}

func TestEmptyFolderRefusesOtherFolders(t *testing.T) {
	db := seedFolderEmptyTest(t, 1)
	ctx := context.Background()
	for _, folderID := range []string{"inbox", "main-acc-inbox", "starred", "archive", ""} {
		if _, err := db.EmptyFolderForUser(ctx, folderID, "default", nil); !errors.Is(err, ErrFolderNotEmptiable) {
			t.Fatalf("EmptyFolderForUser(%q) error = %v, want ErrFolderNotEmptiable", folderID, err)
		}
	}
	if _, err := db.EmptyFolderForUser(ctx, "other-acc-trash", "default", nil); err == nil {
		t.Fatal("EmptyFolderForUser(another user's Trash) error = nil, want not found")
	}
	if got := shownInFolder(t, db, "other-acc-trash"); got != 1 {
		t.Fatalf("another user's Trash = %d, want 1", got)
	}
}

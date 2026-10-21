package storage

import (
	"context"
	"slices"
	"testing"
	"time"
)

// archiveBothCopies syncs both copies of mail sent to oneself into Archive,
// the Sent copy first.
func archiveBothCopies(t *testing.T, db *DB) int64 {
	t.Helper()
	msgID := seedSelfSentCopies(t, db)
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	if _, err := db.UpsertProviderSyncMessages(context.Background(), []ProviderSyncMessage{
		{AccountID: "acc", FolderID: "acc_archive", ProviderMessageID: "email-sent", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: now, IsRead: true},
		{AccountID: "acc", FolderID: "acc_archive", ProviderMessageID: "email-inbox", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: now},
	}); err != nil {
		t.Fatalf("UpsertProviderSyncMessages() error = %v", err)
	}
	return msgID
}

type folderCopyState struct {
	visible bool
	copy    string
	others  []string
}

func folderCopiesOf(t *testing.T, db *DB, msgID int64, folderID string) folderCopyState {
	t.Helper()
	ctx := context.Background()
	var state folderCopyState
	if err := db.Read().QueryRowContext(ctx, `SELECT is_deleted = 0, COALESCE(remote_message_id, '') FROM message_folder_state WHERE message_id = ? AND folder_id = ?`,
		msgID, folderID).Scan(&state.visible, &state.copy); err != nil {
		t.Fatalf("query %s state: %v", folderID, err)
	}
	rows, err := db.Read().QueryContext(ctx, `SELECT remote_message_id FROM message_folder_copies WHERE message_id = ? AND folder_id = ? ORDER BY 1`, msgID, folderID)
	if err != nil {
		t.Fatalf("query %s copies: %v", folderID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan copy: %v", err)
		}
		state.others = append(state.others, id)
	}
	return state
}

func wantFolderCopies(t *testing.T, step string, got folderCopyState, visible bool, copy string, others ...string) {
	t.Helper()
	if got.visible != visible || got.copy != copy || !slices.Equal(got.others, others) {
		t.Fatalf("%s: folder state = %+v, want visible=%v copy=%q others=%v", step, got, visible, copy, others)
	}
}

func TestProviderSyncKeepsTwoCopiesInOneFolder(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	msgID := archiveBothCopies(t, db)

	wantFolderCopies(t, "both archived", folderCopiesOf(t, db, msgID, "acc_archive"), true, "email-sent", "email-inbox")
	wantFolderCopies(t, "Inbox", folderCopiesOf(t, db, msgID, "acc_inbox"), false, "email-inbox")
	wantFolderCopies(t, "Sent", folderCopiesOf(t, db, msgID, "acc_sent"), false, "email-sent")
	var read bool
	if err := db.Read().QueryRowContext(ctx, `SELECT is_read FROM message_folder_state WHERE message_id = ? AND folder_id = 'acc_archive'`, msgID).Scan(&read); err != nil || !read {
		t.Fatalf("Archive state read = %v, %v; want the Sent copy's flags", read, err)
	}

	// Syncing either copy again changes nothing.
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	if _, err := db.UpsertProviderSyncMessages(ctx, []ProviderSyncMessage{
		{AccountID: "acc", FolderID: "acc_archive", ProviderMessageID: "email-inbox", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: now},
	}); err != nil {
		t.Fatalf("UpsertProviderSyncMessages() error = %v", err)
	}
	if _, err := db.UpsertExistingProviderFolderStates(ctx, "acc", "acc_archive", []string{"email-inbox", "email-sent"}); err != nil {
		t.Fatalf("UpsertExistingProviderFolderStates() error = %v", err)
	}
	wantFolderCopies(t, "resynced", folderCopiesOf(t, db, msgID, "acc_archive"), true, "email-sent", "email-inbox")

	// Destroying the state's copy hands the state to the other one.
	if _, err := db.MarkProviderMessageDeleted(ctx, "acc", "email-sent"); err != nil {
		t.Fatalf("MarkProviderMessageDeleted() error = %v", err)
	}
	wantFolderCopies(t, "Sent copy destroyed", folderCopiesOf(t, db, msgID, "acc_archive"), true, "email-inbox")
	if _, err := db.MarkProviderMessageDeleted(ctx, "acc", "email-inbox"); err != nil {
		t.Fatalf("MarkProviderMessageDeleted() error = %v", err)
	}
	wantFolderCopies(t, "both destroyed", folderCopiesOf(t, db, msgID, "acc_archive"), false, "email-inbox")
}

func TestProviderCopyLeavingAFolderWithTwoCopies(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		leave  func(ctx context.Context, db *DB) error
		copy   string
		others []string
	}{
		{"state's copy moved by sync", func(ctx context.Context, db *DB) error {
			_, err := db.UpsertProviderSyncMessages(ctx, []ProviderSyncMessage{
				{AccountID: "acc", FolderID: "acc_inbox", ProviderMessageID: "email-sent", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: now},
			})
			return err
		}, "email-inbox", nil},
		{"other copy moved by sync", func(ctx context.Context, db *DB) error {
			_, err := db.UpsertProviderSyncMessages(ctx, []ProviderSyncMessage{
				{AccountID: "acc", FolderID: "acc_inbox", ProviderMessageID: "email-inbox", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: now},
			})
			return err
		}, "email-sent", nil},
		{"state's copy removed", func(ctx context.Context, db *DB) error {
			return db.MarkProviderMessageRemovedFromFolder(ctx, "acc", "acc_archive", "email-sent")
		}, "email-inbox", nil},
		{"other copy removed", func(ctx context.Context, db *DB) error {
			return db.MarkProviderMessageRemovedFromFolder(ctx, "acc", "acc_archive", "email-inbox")
		}, "email-sent", nil},
		{"state's copy missing from folder", func(ctx context.Context, db *DB) error {
			_, err := db.MarkProviderMessagesMissingFromFolder(ctx, "acc", "acc_archive", map[string]bool{"email-inbox": true})
			return err
		}, "email-inbox", nil},
		{"other copy unseen in folder", func(ctx context.Context, db *DB) error {
			return db.ReconcileProviderFolderSeen(ctx, "acc", "acc_archive", []string{"email-sent"})
		}, "email-sent", nil},
		{"both copies still seen", func(ctx context.Context, db *DB) error {
			return db.ReconcileProviderFolderSeen(ctx, "acc", "acc_archive", []string{"email-sent", "email-inbox"})
		}, "email-sent", []string{"email-inbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := newContactsTestDB(t)
			msgID := archiveBothCopies(t, db)
			if err := tc.leave(ctx, db); err != nil {
				t.Fatalf("leave: %v", err)
			}
			wantFolderCopies(t, tc.name, folderCopiesOf(t, db, msgID, "acc_archive"), true, tc.copy, tc.others...)
		})
	}

	t.Run("neither copy seen", func(t *testing.T) {
		ctx := context.Background()
		db := newContactsTestDB(t)
		msgID := archiveBothCopies(t, db)
		if _, err := db.MarkProviderMessagesMissingFromFolder(ctx, "acc", "acc_archive", nil); err != nil {
			t.Fatalf("MarkProviderMessagesMissingFromFolder() error = %v", err)
		}
		wantFolderCopies(t, "neither seen", folderCopiesOf(t, db, msgID, "acc_archive"), false, "email-sent")
	})
}

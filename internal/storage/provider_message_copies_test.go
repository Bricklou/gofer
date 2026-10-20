package storage

import (
	"context"
	"maps"
	"testing"
	"time"
)

func TestProviderSyncKeepsCopiesWithSharedMessageID(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)

	if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, email_address) VALUES ('acc', 'default', 'user@example.com')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.UpsertFolders(ctx, []UpsertFolderInput{
		{ID: "acc_inbox", AccountID: "acc", RemoteID: "INBOX", ProviderRemoteID: "mb-inbox", Name: "Inbox", Role: "inbox", Selectable: true},
		{ID: "acc_sent", AccountID: "acc", RemoteID: "Sent", ProviderRemoteID: "mb-sent", Name: "Sent", Role: "sent", Selectable: true},
	}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	copyIn := func(folderID, providerID string) ProviderSyncMessage {
		return ProviderSyncMessage{
			AccountID:         "acc",
			FolderID:          folderID,
			ProviderMessageID: providerID,
			InternetMessageID: "<note-to-self@example.com>",
			Subject:           "Note to self",
			FromEmail:         "user@example.com",
			DateSent:          now,
			ToRecipients:      []Recipient{{Email: "user@example.com"}},
		}
	}
	sent, inbox := copyIn("acc_sent", "email-sent"), copyIn("acc_inbox", "email-inbox")

	visibleFolders := func() map[string]bool {
		t.Helper()
		rows, err := db.Read().QueryContext(ctx, `
			SELECT mfs.folder_id FROM message_folder_state mfs
			JOIN messages m ON m.id = mfs.message_id
			WHERE m.account_id = 'acc' AND m.internet_message_id = '<note-to-self@example.com>' AND mfs.is_deleted = 0`)
		if err != nil {
			t.Fatalf("query folder states: %v", err)
		}
		defer rows.Close()
		got := map[string]bool{}
		for rows.Next() {
			var folderID string
			if err := rows.Scan(&folderID); err != nil {
				t.Fatalf("scan folder state: %v", err)
			}
			got[folderID] = true
		}
		return got
	}
	wantBoth := func(step string) {
		t.Helper()
		if got := visibleFolders(); !got["acc_inbox"] || !got["acc_sent"] || len(got) != 2 {
			t.Fatalf("%s: visible folders = %v, want Inbox and Sent", step, got)
		}
	}

	for _, step := range []struct {
		name string
		msgs []ProviderSyncMessage
	}{
		{"both copies", []ProviderSyncMessage{sent, inbox}},
		{"resync Sent copy", []ProviderSyncMessage{sent}},
		{"resync Inbox copy", []ProviderSyncMessage{inbox}},
	} {
		if _, err := db.UpsertProviderSyncMessages(ctx, step.msgs); err != nil {
			t.Fatalf("%s: UpsertProviderSyncMessages() error = %v", step.name, err)
		}
		wantBoth(step.name)
	}

	var rowCount int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE account_id = 'acc'`).Scan(&rowCount); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("message rows = %d, want 1", rowCount)
	}

	if _, err := db.MarkProviderMessageDeleted(ctx, "acc", "email-inbox"); err != nil {
		t.Fatalf("MarkProviderMessageDeleted() error = %v", err)
	}
	if got := visibleFolders(); !got["acc_sent"] || got["acc_inbox"] {
		t.Fatalf("after deleting Inbox copy: visible folders = %v, want only Sent", got)
	}
}

func TestProviderFolderReconcileOnlyTouchesTheSameCopy(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, email_address, provider) VALUES ('acc', 'default', 'user@example.com', 'jmap')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.UpsertFolders(ctx, []UpsertFolderInput{
		{ID: "acc_inbox", AccountID: "acc", RemoteID: "INBOX", ProviderRemoteID: "mb-inbox", Name: "Inbox", Role: "inbox", Selectable: true},
		{ID: "acc_sent", AccountID: "acc", RemoteID: "Sent", ProviderRemoteID: "mb-sent", Name: "Sent", Role: "sent", Selectable: true},
		{ID: "acc_work", AccountID: "acc", RemoteID: "Work", ProviderRemoteID: "mb-work", Name: "Work", Selectable: true},
	}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	message := func(folderID, providerID, internetID string) ProviderSyncMessage {
		return ProviderSyncMessage{AccountID: "acc", FolderID: folderID, ProviderMessageID: providerID, InternetMessageID: internetID, Subject: "Plans", DateSent: now}
	}
	if _, err := db.UpsertProviderSyncMessages(ctx, []ProviderSyncMessage{
		message("acc_sent", "email-sent", "<self@example.com>"),
		message("acc_inbox", "email-inbox", "<self@example.com>"),
		// One email in two mailboxes is one copy.
		message("acc_inbox", "email-both", "<both@example.com>"),
		message("acc_work", "email-both", "<both@example.com>"),
	}); err != nil {
		t.Fatalf("UpsertProviderSyncMessages() error = %v", err)
	}
	visible := func(internetID string) map[string]string {
		t.Helper()
		rows, err := db.Read().QueryContext(ctx, `
			SELECT mfs.folder_id, COALESCE(mfs.remote_message_id, '') FROM message_folder_state mfs
			JOIN messages m ON m.id = mfs.message_id
			WHERE m.internet_message_id = ? AND mfs.is_deleted = 0`, internetID)
		if err != nil {
			t.Fatalf("query folder states: %v", err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var folderID, providerID string
			if err := rows.Scan(&folderID, &providerID); err != nil {
				t.Fatalf("scan folder state: %v", err)
			}
			got[folderID] = providerID
		}
		return got
	}
	if got, want := visible("<both@example.com>"), map[string]string{"acc_inbox": "email-both", "acc_work": "email-both"}; !maps.Equal(got, want) {
		t.Fatalf("email in two mailboxes = %v, want %v", got, want)
	}

	// A full import of Sent that sees only the Sent copy hides nothing.
	if _, err := db.MarkProviderMessagesMissingFromFolder(ctx, "acc", "acc_sent", map[string]bool{"email-sent": true}); err != nil {
		t.Fatalf("MarkProviderMessagesMissingFromFolder() error = %v", err)
	}
	if got, want := visible("<self@example.com>"), map[string]string{"acc_sent": "email-sent", "acc_inbox": "email-inbox"}; !maps.Equal(got, want) {
		t.Fatalf("after importing Sent = %v, want %v", got, want)
	}
	// Moving the shared email out of Work hides only that mailbox.
	if err := db.MarkProviderMessageRemovedFromFolder(ctx, "acc", "acc_work", "email-both"); err != nil {
		t.Fatalf("MarkProviderMessageRemovedFromFolder() error = %v", err)
	}
	if got, want := visible("<both@example.com>"), map[string]string{"acc_inbox": "email-both"}; !maps.Equal(got, want) {
		t.Fatalf("after leaving Work = %v, want %v", got, want)
	}
	// A full import of the Inbox that no longer sees the received copy hides
	// it, and the Sent copy stays.
	if _, err := db.MarkProviderMessagesMissingFromFolder(ctx, "acc", "acc_inbox", map[string]bool{"email-both": true}); err != nil {
		t.Fatalf("MarkProviderMessagesMissingFromFolder() error = %v", err)
	}
	if got, want := visible("<self@example.com>"), map[string]string{"acc_sent": "email-sent"}; !maps.Equal(got, want) {
		t.Fatalf("after importing the Inbox = %v, want %v", got, want)
	}
}

// seedSelfSentCopies stores the Sent and Inbox copies of mail sent to oneself
// on a JMAP account, and returns their row.
func seedSelfSentCopies(t *testing.T, db *DB) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, email_address, provider) VALUES ('acc', 'default', 'user@example.com', 'jmap')`); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if err := db.UpsertFolders(ctx, []UpsertFolderInput{
		{ID: "acc_inbox", AccountID: "acc", RemoteID: "INBOX", ProviderRemoteID: "mb-inbox", Name: "Inbox", Role: "inbox", Selectable: true},
		{ID: "acc_sent", AccountID: "acc", RemoteID: "Sent", ProviderRemoteID: "mb-sent", Name: "Sent", Role: "sent", Selectable: true},
		{ID: "acc_archive", AccountID: "acc", RemoteID: "Archive", ProviderRemoteID: "mb-archive", Name: "Archive", Role: "archive", Selectable: true},
	}); err != nil {
		t.Fatalf("UpsertFolders() error = %v", err)
	}
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	ids, err := db.UpsertProviderSyncMessages(ctx, []ProviderSyncMessage{
		{AccountID: "acc", FolderID: "acc_sent", ProviderMessageID: "email-sent", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: now, IsRead: true},
		{AccountID: "acc", FolderID: "acc_inbox", ProviderMessageID: "email-inbox", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: now},
	})
	if err != nil {
		t.Fatalf("UpsertProviderSyncMessages() error = %v", err)
	}
	return ids["email-sent"]
}

func queuedMutationTargets(t *testing.T, db *DB, messageID int64, kind string) map[string]string {
	t.Helper()
	rows, err := db.Read().QueryContext(context.Background(), `SELECT folder_id FROM message_mutations WHERE message_id = ? AND kind = ?`, messageID, kind)
	if err != nil {
		t.Fatalf("query mutations: %v", err)
	}
	var folders []string
	for rows.Next() {
		var folderID string
		if err := rows.Scan(&folderID); err != nil {
			t.Fatalf("scan mutation: %v", err)
		}
		folders = append(folders, folderID)
	}
	rows.Close()
	targets := map[string]string{}
	for _, folderID := range folders {
		info, err := db.GetMessageMutationInfoIncludingDeletedInFolder(context.Background(), messageID, folderID)
		if err != nil || info == nil {
			t.Fatalf("GetMessageMutationInfoIncludingDeletedInFolder(%q) = %v, %v", folderID, info, err)
		}
		targets[folderID] = info.RemoteMessageID
	}
	return targets
}

func TestProviderMutationsGoToTheCopyTheyAreMadeOn(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	msgID := seedSelfSentCopies(t, db)

	for folderID, want := range map[string]string{"acc_sent": "email-sent", "acc_inbox": "email-inbox"} {
		info, err := db.GetMessageMutationInfoForFolder(ctx, msgID, folderID)
		if err != nil || info == nil || info.RemoteMessageID != want {
			t.Fatalf("mutation info in %s = %+v, %v; want %s", folderID, info, err, want)
		}
	}

	// Reading the message marks both copies read on the server.
	if err := db.SetMessageReadAndQueue(ctx, msgID, true); err != nil {
		t.Fatalf("SetMessageReadAndQueue() error = %v", err)
	}
	if got, want := queuedMutationTargets(t, db, msgID, MessageMutationRead), map[string]string{"acc_sent": "email-sent", "acc_inbox": "email-inbox"}; !maps.Equal(got, want) {
		t.Fatalf("read changes = %v, want %v", got, want)
	}

	// Moving the Inbox copy leaves the Sent one, and the moved copy keeps its id.
	if err := db.MoveMessageAndQueue(ctx, msgID, "acc_inbox", "acc_archive"); err != nil {
		t.Fatalf("MoveMessageAndQueue() error = %v", err)
	}
	if got, want := visibleCopies(t, db, "<self@example.com>"), map[string]string{"acc_sent": "email-sent", "acc_archive": "email-inbox"}; !maps.Equal(got, want) {
		t.Fatalf("after moving the Inbox copy = %v, want %v", got, want)
	}

	// Deleting the archived copy leaves the Sent one.
	if err := db.PermanentlyDeleteMessageAndQueue(ctx, msgID, "acc_archive"); err != nil {
		t.Fatalf("PermanentlyDeleteMessageAndQueue() error = %v", err)
	}
	if got, want := visibleCopies(t, db, "<self@example.com>"), map[string]string{"acc_sent": "email-sent"}; !maps.Equal(got, want) {
		t.Fatalf("after deleting the archived copy = %v, want %v", got, want)
	}
	if got, want := queuedMutationTargets(t, db, msgID, MessageMutationDelete), map[string]string{"acc_archive": "email-inbox"}; !maps.Equal(got, want) {
		t.Fatalf("delete = %v, want %v", got, want)
	}
}

func TestProviderMoveGivesTheMovedCopyItsNewID(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	msgID := seedSelfSentCopies(t, db)
	if err := db.MoveMessageAndQueue(ctx, msgID, "acc_inbox", "acc_archive"); err != nil {
		t.Fatalf("MoveMessageAndQueue() error = %v", err)
	}
	var mutationID string
	if err := db.Read().QueryRowContext(ctx, `SELECT id FROM message_mutations WHERE message_id = ? AND kind = ?`, msgID, MessageMutationMove).Scan(&mutationID); err != nil {
		t.Fatalf("query move: %v", err)
	}
	// Outlook gives a moved message a new id.
	if err := db.AdvanceMessageMoveMutation(ctx, mutationID, "acc_archive", "email-archived", 0); err != nil {
		t.Fatalf("AdvanceMessageMoveMutation() error = %v", err)
	}
	if got, want := visibleCopies(t, db, "<self@example.com>"), map[string]string{"acc_sent": "email-sent", "acc_archive": "email-archived"}; !maps.Equal(got, want) {
		t.Fatalf("after the move = %v, want %v", got, want)
	}
}

func visibleCopies(t *testing.T, db *DB, internetID string) map[string]string {
	t.Helper()
	rows, err := db.Read().QueryContext(context.Background(), `
		SELECT mfs.folder_id, COALESCE(mfs.remote_message_id, '') FROM message_folder_state mfs
		JOIN messages m ON m.id = mfs.message_id
		WHERE m.internet_message_id = ? AND mfs.is_deleted = 0`, internetID)
	if err != nil {
		t.Fatalf("query folder states: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var folderID, providerID string
		if err := rows.Scan(&folderID, &providerID); err != nil {
			t.Fatalf("scan folder state: %v", err)
		}
		got[folderID] = providerID
	}
	return got
}

func TestProviderContentIsFetchedThroughALiveCopy(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	msgID := seedSelfSentCopies(t, db)
	snapshot, err := db.GetMessageContentSnapshotForUser(ctx, "default", msgID)
	if err != nil || snapshot.RemoteMessageID != "email-inbox" {
		t.Fatalf("content snapshot = %+v, %v; want the Inbox copy", snapshot, err)
	}
	// Once the Inbox copy leaves, content comes through the Sent copy.
	if _, err := db.MarkProviderMessagesMissingFromFolder(ctx, "acc", "acc_inbox", map[string]bool{}); err != nil {
		t.Fatalf("MarkProviderMessagesMissingFromFolder() error = %v", err)
	}
	snapshot, err = db.GetMessageContentSnapshotForUser(ctx, "default", msgID)
	if err != nil || snapshot.RemoteMessageID != "email-sent" {
		t.Fatalf("content snapshot = %+v, %v; want the Sent copy", snapshot, err)
	}
}

func TestProviderChangesQueueOncePerCopy(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	msgID := seedSelfSentCopies(t, db)

	// Moving each copy out of its folder queues two moves.
	if err := db.MoveMessageAndQueue(ctx, msgID, "acc_inbox", "acc_archive"); err != nil {
		t.Fatalf("MoveMessageAndQueue(Inbox) error = %v", err)
	}
	if err := db.MoveMessageAndQueue(ctx, msgID, "acc_sent", "acc_archive"); err != nil {
		t.Fatalf("MoveMessageAndQueue(Sent) error = %v", err)
	}
	var moves int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_mutations WHERE message_id = ? AND kind = 'move'`, msgID).Scan(&moves); err != nil || moves != 2 {
		t.Fatalf("queued moves = %d, %v; want one per copy", moves, err)
	}
	if _, err := db.Write().ExecContext(ctx, `DELETE FROM message_mutations`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write().ExecContext(ctx, `UPDATE message_folder_state SET is_deleted = (folder_id = 'acc_archive') WHERE message_id = ?`, msgID); err != nil {
		t.Fatal(err)
	}

	// Deleting both copies before either delete is sent queues both.
	if err := db.PermanentlyDeleteMessageAndQueue(ctx, msgID, "acc_sent"); err != nil {
		t.Fatalf("PermanentlyDeleteMessageAndQueue(Sent) error = %v", err)
	}
	if err := db.PermanentlyDeleteMessageAndQueue(ctx, msgID, "acc_inbox"); err != nil {
		t.Fatalf("PermanentlyDeleteMessageAndQueue(Inbox) error = %v", err)
	}
	if got, want := queuedMutationTargets(t, db, msgID, MessageMutationDelete), map[string]string{"acc_sent": "email-sent", "acc_inbox": "email-inbox"}; !maps.Equal(got, want) {
		t.Fatalf("deletes = %v, want %v", got, want)
	}
	// A sync of the Sent copy before its delete is sent keeps it hidden.
	if _, err := db.UpsertProviderSyncMessages(ctx, []ProviderSyncMessage{
		{AccountID: "acc", FolderID: "acc_sent", ProviderMessageID: "email-sent", InternetMessageID: "<self@example.com>", Subject: "Plans", DateSent: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if got := visibleCopies(t, db, "<self@example.com>"); len(got) != 0 {
		t.Fatalf("copies shown while their deletes are pending = %v", got)
	}
}

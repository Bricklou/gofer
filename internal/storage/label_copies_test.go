package storage

import (
	"context"
	"slices"
	"testing"
	"time"
)

// syncCopyLabels syncs one copy of mail sent to oneself with its keywords.
func syncCopyLabels(t *testing.T, db *DB, folderID, providerMessageID string, names ...string) {
	t.Helper()
	labels := make([]LabelInput, 0, len(names))
	for _, name := range names {
		labels = append(labels, LabelInput{Name: name, ProviderID: name, ProviderType: LabelProviderKeyword})
	}
	if _, err := db.UpsertProviderSyncMessages(context.Background(), []ProviderSyncMessage{{
		AccountID: "acc", FolderID: folderID, ProviderMessageID: providerMessageID, InternetMessageID: "<self@example.com>",
		Subject: "Plans", DateSent: time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		LabelsKnown: true, LabelProvider: LabelProviderKeyword, Labels: labels,
	}}); err != nil {
		t.Fatalf("UpsertProviderSyncMessages() error = %v", err)
	}
}

func wantMessageLabels(t *testing.T, db *DB, step string, msgID int64, want ...string) {
	t.Helper()
	rows, err := db.Read().QueryContext(context.Background(), `
		SELECT l.name FROM message_labels ml JOIN labels l ON l.id = ml.label_id
		WHERE ml.message_id = ? ORDER BY l.name`, msgID)
	if err != nil {
		t.Fatalf("query labels: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan label: %v", err)
		}
		got = append(got, name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s: labels = %v, want %v", step, got, want)
	}
}

func labelCopyCount(t *testing.T, db *DB, msgID int64) int {
	t.Helper()
	var count int
	if err := db.Read().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM message_label_copies WHERE message_id = ?`, msgID).Scan(&count); err != nil {
		t.Fatalf("count label copies: %v", err)
	}
	return count
}

func TestProviderSyncKeepsLabelsPerCopy(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	msgID := seedSelfSentCopies(t, db)

	syncCopyLabels(t, db, "acc_sent", "email-sent", "Work")
	syncCopyLabels(t, db, "acc_inbox", "email-inbox", "Travel")
	wantMessageLabels(t, db, "each copy labelled", msgID, "Travel", "Work")

	// A label goes once no copy carries it.
	syncCopyLabels(t, db, "acc_sent", "email-sent")
	wantMessageLabels(t, db, "Work taken off the Sent copy", msgID, "Travel")

	// It stays while another copy still carries it.
	syncCopyLabels(t, db, "acc_sent", "email-sent", "Travel")
	syncCopyLabels(t, db, "acc_inbox", "email-inbox")
	wantMessageLabels(t, db, "Travel taken off the Inbox copy", msgID, "Travel")

	// A label sync reaches only the copy it read.
	if err := db.ReplaceMessageLabelsForProvider(ctx, msgID, "acc", LabelProviderKeyword, "email-inbox", []LabelInput{{Name: "Family", ProviderID: "Family"}}); err != nil {
		t.Fatalf("ReplaceMessageLabelsForProvider() error = %v", err)
	}
	wantMessageLabels(t, db, "label sync of the Inbox copy", msgID, "Family", "Travel")

	// The labels only a destroyed copy carried go with it, and the row's
	// labels are its last copy's own.
	if _, err := db.MarkProviderMessageDeleted(ctx, "acc", "email-inbox"); err != nil {
		t.Fatalf("MarkProviderMessageDeleted() error = %v", err)
	}
	wantMessageLabels(t, db, "Inbox copy destroyed", msgID, "Travel")
	if n := labelCopyCount(t, db, msgID); n != 0 {
		t.Fatalf("label copies after one copy is left = %d, want 0", n)
	}
	syncCopyLabels(t, db, "acc_sent", "email-sent")
	wantMessageLabels(t, db, "last copy synced", msgID)
}

func TestFirstSyncOfASecondCopyCreditsLabelsToTheOther(t *testing.T) {
	db := newContactsTestDB(t)
	msgID := seedSelfSentCopies(t, db)
	if _, err := db.MarkProviderMessageDeleted(context.Background(), "acc", "email-inbox"); err != nil {
		t.Fatalf("MarkProviderMessageDeleted() error = %v", err)
	}
	syncCopyLabels(t, db, "acc_sent", "email-sent", "Work")
	wantMessageLabels(t, db, "one copy", msgID, "Work")

	// The Inbox copy comes back without the label, which the Sent copy has.
	syncCopyLabels(t, db, "acc_inbox", "email-inbox")
	wantMessageLabels(t, db, "second copy without it", msgID, "Work")

	syncCopyLabels(t, db, "acc_sent", "email-sent")
	wantMessageLabels(t, db, "taken off the Sent copy", msgID)
}

func TestFolderMissingACopyDropsTheLabelsOnlyItCarried(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	msgID := seedSelfSentCopies(t, db)
	syncCopyLabels(t, db, "acc_sent", "email-sent", "Work")
	syncCopyLabels(t, db, "acc_inbox", "email-inbox", "Travel")

	if _, err := db.MarkProviderMessagesMissingFromFolder(ctx, "acc", "acc_inbox", map[string]bool{}); err != nil {
		t.Fatalf("MarkProviderMessagesMissingFromFolder() error = %v", err)
	}
	wantMessageLabels(t, db, "Inbox copy missing", msgID, "Work")
}

package storage

import (
	"strings"
	"testing"
)

// staleFolderThreadUpdateTrigger is the update trigger as a development build
// of schema version 108 created it.
const staleFolderThreadUpdateTrigger = `
CREATE TRIGGER folder_thread_track_state_update
AFTER UPDATE ON message_folder_state
WHEN OLD.is_read IS NOT NEW.is_read OR OLD.is_starred IS NOT NEW.is_starred
  OR OLD.is_deleted IS NOT NEW.is_deleted OR OLD.folder_id IS NOT NEW.folder_id
  OR OLD.message_id IS NOT NEW.message_id
BEGIN
    INSERT OR IGNORE INTO folder_thread_dirty(folder_id, thread_key)
    SELECT OLD.folder_id, COALESCE(NULLIF(m.thread_id, ''), printf('msg:%d', m.id))
    FROM messages m WHERE m.id = OLD.message_id
      AND NOT EXISTS(SELECT 1 FROM folder_thread_dirty w WHERE w.folder_id = OLD.folder_id AND w.thread_key = '*');
    INSERT OR IGNORE INTO folder_thread_dirty(folder_id, thread_key)
    SELECT NEW.folder_id, COALESCE(NULLIF(m.thread_id, ''), printf('msg:%d', m.id))
    FROM messages m WHERE m.id = NEW.message_id
      AND NOT EXISTS(SELECT 1 FROM folder_thread_dirty w WHERE w.folder_id = NEW.folder_id AND w.thread_key = '*');
END`

func TestRepairFolderThreadTriggersLetsMailMoveBackToAFolderItLeft(t *testing.T) {
	db, messageID := seedMoveMutationTest(t, "imap")
	ctx := t.Context()
	if _, err := db.Write().ExecContext(ctx, `DROP TRIGGER folder_thread_track_state_update`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, staleFolderThreadUpdateTrigger); err != nil {
		t.Fatalf("create stale trigger: %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `DELETE FROM folder_thread_dirty`); err != nil {
		t.Fatalf("clear dirty: %v", err)
	}

	if err := db.MoveMessagesAndQueueForUser(ctx, []int64{messageID}, "inbox", "archive", "default"); err != nil {
		t.Fatalf("move to archive: %v", err)
	}
	if err := db.MoveMessagesAndQueueForUser(ctx, []int64{messageID}, "archive", "inbox", "default"); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed: folder_thread_dirty") {
		t.Fatalf("move back with the stale trigger = %v, want the duplicate failure this repair is for", err)
	}

	if err := repairFolderThreadTriggers(db.Write()); err != nil {
		t.Fatalf("repairFolderThreadTriggers() error = %v", err)
	}
	var stale int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND sql LIKE '%INSERT OR IGNORE INTO folder_thread_dirty%'`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("stale triggers after repair = %d, %v; want 0", stale, err)
	}
	var triggers int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'folder_thread_track_%'`).Scan(&triggers); err != nil || triggers != len(folderThreadTriggers) {
		t.Fatalf("triggers after repair = %d, %v; want %d", triggers, err, len(folderThreadTriggers))
	}
	if err := db.MoveMessagesAndQueueForUser(ctx, []int64{messageID}, "archive", "inbox", "default"); err != nil {
		t.Fatalf("move back after repair: %v", err)
	}
}

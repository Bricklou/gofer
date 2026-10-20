package storage

import (
	"context"
	"testing"
)

func TestMigrateV117ToV118GivesProviderFolderStatesTheirRowID(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().ExecContext(ctx, `
		ALTER TABLE messages ADD COLUMN remote_message_id TEXT;
		INSERT INTO accounts (id, user_id, email_address, provider) VALUES
			('jmap', 'default', 'jmap@example.com', 'jmap'),
			('imap', 'default', 'imap@example.com', 'imap');
		INSERT INTO folders (id, account_id, remote_id, name) VALUES
			('jmap_inbox', 'jmap', 'INBOX', 'Inbox'),
			('jmap_work', 'jmap', 'Work', 'Work'),
			('imap_inbox', 'imap', 'INBOX', 'Inbox');
		INSERT INTO messages (id, account_id, remote_message_id, internet_message_id) VALUES
			(1, 'jmap', 'email-1', '<one@example.com>'),
			(2, 'imap', 'stray-id', '<two@example.com>');
		INSERT INTO message_folder_state (message_id, folder_id, remote_uid) VALUES
			(1, 'jmap_inbox', NULL),
			(1, 'jmap_work', NULL),
			(2, 'imap_inbox', 7);
		DROP INDEX idx_message_folder_state_remote_message;
		ALTER TABLE message_folder_state DROP COLUMN remote_message_id;
		DELETE FROM schema_version;
		INSERT INTO schema_version (version) VALUES (117);`); err != nil {
		t.Fatalf("set up schema 117: %v", err)
	}
	if err := migrateV117ToV118(db.Write()); err != nil {
		t.Fatalf("migrateV117ToV118() error = %v", err)
	}
	rows, err := db.Read().QueryContext(ctx, `SELECT folder_id, COALESCE(remote_message_id, '') FROM message_folder_state ORDER BY folder_id`)
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
	want := map[string]string{"jmap_inbox": "email-1", "jmap_work": "email-1", "imap_inbox": ""}
	for folderID, providerID := range want {
		if got[folderID] != providerID {
			t.Fatalf("folder state provider ids = %v, want %v", got, want)
		}
	}
	if err := migrateV117ToV118(db.Write()); err != nil {
		t.Fatalf("migrateV117ToV118() rerun error = %v", err)
	}
}

func TestMigrateV118ToV119DropsTheRowProviderID(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().ExecContext(ctx, `
		ALTER TABLE messages ADD COLUMN remote_message_id TEXT;
		CREATE UNIQUE INDEX idx_messages_remote_id ON messages(account_id, remote_message_id);
		INSERT INTO accounts (id, user_id, email_address, provider) VALUES ('jmap', 'default', 'jmap@example.com', 'jmap');
		INSERT INTO folders (id, account_id, remote_id, name) VALUES ('jmap_inbox', 'jmap', 'INBOX', 'Inbox');
		INSERT INTO messages (id, account_id, remote_message_id, internet_message_id) VALUES (1, 'jmap', 'email-1', '<one@example.com>');
		INSERT INTO message_folder_state (message_id, folder_id, remote_message_id) VALUES (1, 'jmap_inbox', 'email-1');
		DELETE FROM schema_version;
		INSERT INTO schema_version (version) VALUES (118);`); err != nil {
		t.Fatalf("set up schema 118: %v", err)
	}
	if err := migrateV118ToV119(db.Write()); err != nil {
		t.Fatalf("migrateV118ToV119() error = %v", err)
	}
	var columns int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'remote_message_id'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("messages.remote_message_id still there: %d, %v", columns, err)
	}
	var providerID string
	if err := db.Read().QueryRowContext(ctx, `SELECT remote_message_id FROM message_folder_state WHERE message_id = 1`).Scan(&providerID); err != nil || providerID != "email-1" {
		t.Fatalf("folder state provider id = %q, %v", providerID, err)
	}
	if err := migrateV118ToV119(db.Write()); err != nil {
		t.Fatalf("migrateV118ToV119() rerun error = %v", err)
	}
}

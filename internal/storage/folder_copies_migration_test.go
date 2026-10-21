package storage

import (
	"context"
	"testing"
)

func TestMigrateV119ToV120AddsFolderCopies(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().ExecContext(ctx, `
		DROP TABLE message_folder_copies;
		DELETE FROM schema_version;
		INSERT INTO schema_version (version) VALUES (119);
		INSERT INTO accounts (id, user_id, email_address, provider) VALUES ('jmap', 'default', 'jmap@example.com', 'jmap');
		INSERT INTO folders (id, account_id, remote_id, name) VALUES ('jmap_trash', 'jmap', 'Trash', 'Trash');
		INSERT INTO messages (id, account_id, internet_message_id) VALUES (1, 'jmap', '<one@example.com>');
		INSERT INTO message_folder_state (message_id, folder_id, remote_message_id) VALUES (1, 'jmap_trash', 'email-1');`); err != nil {
		t.Fatalf("set up schema 119: %v", err)
	}
	if err := migrateV119ToV120(db.Write()); err != nil {
		t.Fatalf("migrateV119ToV120() error = %v", err)
	}
	if err := migrateV119ToV120(db.Write()); err != nil {
		t.Fatalf("migrateV119ToV120() rerun error = %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO message_folder_copies (message_id, folder_id, remote_message_id) VALUES (1, 'jmap_trash', 'email-2')`); err != nil {
		t.Fatalf("record further copy: %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO message_folder_copies (message_id, folder_id, remote_message_id) VALUES (1, 'jmap_trash', '')`); err == nil {
		t.Fatal("copy without a provider id was accepted")
	}
	if _, err := db.Write().ExecContext(ctx, `DELETE FROM message_folder_state WHERE message_id = 1`); err != nil {
		t.Fatalf("delete folder state: %v", err)
	}
	var copies int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_folder_copies`).Scan(&copies); err != nil || copies != 0 {
		t.Fatalf("copies left after their folder state = %d, %v", copies, err)
	}
}

func TestMigrateV120ToV121AddsLabelCopies(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().ExecContext(ctx, `
		DROP TABLE message_label_copies;
		DELETE FROM schema_version;
		INSERT INTO schema_version (version) VALUES (120);
		INSERT INTO accounts (id, user_id, email_address, provider) VALUES ('jmap', 'default', 'jmap@example.com', 'jmap');
		INSERT INTO labels (id, account_id, name, provider_type) VALUES ('work', 'jmap', 'Work', 'imap_keyword');
		INSERT INTO messages (id, account_id, internet_message_id) VALUES (1, 'jmap', '<one@example.com>');`); err != nil {
		t.Fatalf("set up schema 120: %v", err)
	}
	if err := migrateV120ToV121(db.Write()); err != nil {
		t.Fatalf("migrateV120ToV121() error = %v", err)
	}
	if err := migrateV120ToV121(db.Write()); err != nil {
		t.Fatalf("migrateV120ToV121() rerun error = %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO message_label_copies (message_id, label_id, remote_message_id) VALUES (1, 'work', 'email-1')`); err != nil {
		t.Fatalf("record label copy: %v", err)
	}
	if _, err := db.Write().ExecContext(ctx, `DELETE FROM labels WHERE id = 'work'`); err != nil {
		t.Fatalf("delete label: %v", err)
	}
	var copies int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_label_copies`).Scan(&copies); err != nil || copies != 0 {
		t.Fatalf("label copies left after their label = %d, %v", copies, err)
	}
}

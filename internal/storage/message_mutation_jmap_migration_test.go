package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// Queued changes made before schema 116 survive the table rebuild, and the
// rebuilt table accepts changes for JMAP accounts.
func TestMigrateV116KeepsQueuedMutationsAndAllowsJMAP(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := strings.Replace(messageMutationsTableSQL, "'imap', 'jmap'", "'imap'", 1)
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','imap','robin@example.test'),('jmap-account','owner','jmap','sam@example.test');
INSERT INTO messages(id,account_id,from_email) VALUES(1,'account','sam@example.test'),(2,'jmap-account','robin@example.test');
DROP TABLE message_mutations;
` + old + `;
INSERT INTO message_mutations(id,account_id,message_id,folder_id,provider_type,kind,target_value,status,attempt_count,last_error)
 VALUES('queued','account',1,'inbox','imap','read',1,'failed',2,'server busy'),
 ('misfiled','jmap-account',2,'jmap-inbox','imap','read',1,'failed',5,'message provider changed from imap');
DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(115);`); err != nil {
		t.Fatal(err)
	}
	insertJMAP := `INSERT INTO message_mutations(id,account_id,message_id,folder_id,provider_type,kind,target_value) VALUES(?, 'account', 1, ?, 'jmap', 'starred', 1)`
	if _, err := db.Write().Exec(insertJMAP, "jmap-before", "a"); err == nil {
		t.Fatal("old table accepted a JMAP mutation")
	}
	for range 2 {
		if err := migrateV115ToV116(db.Write()); err != nil {
			t.Fatal(err)
		}
	}
	var kind, status, lastError string
	var attempts int
	if err := db.Read().QueryRow(`SELECT kind,status,attempt_count,last_error FROM message_mutations WHERE id='queued'`).Scan(&kind, &status, &attempts, &lastError); err != nil {
		t.Fatalf("queued mutation after migration: %v", err)
	}
	if kind != "read" || status != "failed" || attempts != 2 || lastError != "server busy" {
		t.Fatalf("queued mutation = %s %s %d %q", kind, status, attempts, lastError)
	}
	// A JMAP account's change queued as IMAP before 116 becomes a JMAP one,
	// ready to try again; the IMAP account's change is untouched.
	var provider string
	if err := db.Read().QueryRow(`SELECT provider_type, status FROM message_mutations WHERE id='misfiled'`).Scan(&provider, &status); err != nil || provider != "jmap" || status != "pending" {
		t.Fatalf("misfiled JMAP change = %s %s, %v", provider, status, err)
	}
	if err := db.Read().QueryRow(`SELECT provider_type FROM message_mutations WHERE id='queued'`).Scan(&provider); err != nil || provider != "imap" {
		t.Fatalf("IMAP change provider = %s, %v", provider, err)
	}
	if _, err := db.Write().Exec(insertJMAP, "jmap-after", "b"); err != nil {
		t.Fatalf("JMAP mutation after migration: %v", err)
	}
	var indexes int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name IN ('idx_message_mutations_due','idx_message_mutations_account')`).Scan(&indexes); err != nil || indexes != 2 {
		t.Fatalf("indexes after migration = %d, %v", indexes, err)
	}
}

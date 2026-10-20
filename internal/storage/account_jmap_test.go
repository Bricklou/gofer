package storage

import (
	"path/filepath"
	"testing"
)

// Accounts that existed before the column get an empty JMAP session URL, and
// running the migration again changes nothing.
func TestAccountJMAPSessionURLMigration(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
 INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','imap','robin@example.test');
 ALTER TABLE accounts DROP COLUMN jmap_session_url; DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(113);`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateV113ToV114(db.Write()); err != nil {
			t.Fatal(err)
		}
	}
	var sessionURL string
	if err := db.Read().QueryRow(`SELECT jmap_session_url FROM accounts WHERE id='account'`).Scan(&sessionURL); err != nil || sessionURL != "" {
		t.Fatalf("existing account: %q %v", sessionURL, err)
	}
	var version int
	if err := db.Read().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 114 {
		t.Fatalf("schema version = %d %v", version, err)
	}
}

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

// Accounts that existed before the column keep provider notifications on.
func TestAccountProviderPushMigration(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
 INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','outlook','robin@example.test');
 ALTER TABLE accounts DROP COLUMN provider_push_enabled; DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(121);`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrateV121ToV122(db.Write()); err != nil {
			t.Fatal(err)
		}
	}
	var enabled int
	if err := db.Read().QueryRow(`SELECT provider_push_enabled FROM accounts WHERE id='account'`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("existing account: %d %v", enabled, err)
	}
	if _, err := db.Write().Exec(`UPDATE accounts SET provider_push_enabled=2 WHERE id='account'`); err == nil {
		t.Fatal("accepted a value other than 0 or 1")
	}
	ctx := t.Context()
	if err := db.SetProviderPushEnabled(ctx, "other", "account", false); err != ErrAccountRoute {
		t.Fatalf("another user changed the account: %v", err)
	}
	if err := db.SetProviderPushEnabled(ctx, "owner", "account", false); err != nil {
		t.Fatal(err)
	}
	if enabled, err := db.ProviderPushEnabled(ctx, "account"); err != nil || enabled {
		t.Fatalf("after choosing checks: %t %v", enabled, err)
	}
	if _, err := db.ProviderPushEnabled(ctx, "missing"); err != ErrAccountRoute {
		t.Fatalf("missing account: %v", err)
	}
	var version int
	if err := db.Read().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 122 {
		t.Fatalf("schema version = %d %v", version, err)
	}
}

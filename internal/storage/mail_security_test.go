package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMailSecurityExceptionsAreExactAndTrackAffectedAccounts(t *testing.T) {
	ctx := context.Background()
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.AddHTTPDiscoveryException(ctx, "LAB.EXAMPLE.TEST.", "admin"); err != nil {
		t.Fatalf("AddHTTPDiscoveryException() error = %v", err)
	}
	allowed, err := db.IsHTTPDiscoveryAllowed(ctx, "lab.example.test")
	if err != nil || !allowed {
		t.Fatalf("IsHTTPDiscoveryAllowed() = %v, %v; want true", allowed, err)
	}
	allowed, err = db.IsHTTPDiscoveryAllowed(ctx, "other.example.test")
	if err != nil || allowed {
		t.Fatalf("other IsHTTPDiscoveryAllowed() = %v, %v; want false", allowed, err)
	}

	if err := db.AddPlaintextTransportException(ctx, "imap", "MAIL.TEST.", 1143, "admin"); err != nil {
		t.Fatalf("AddPlaintextTransportException() error = %v", err)
	}
	allowed, err = db.IsPlaintextTransportAllowed(ctx, "IMAP", "mail.test", 1143)
	if err != nil || !allowed {
		t.Fatalf("IsPlaintextTransportAllowed() = %v, %v; want true", allowed, err)
	}
	allowed, err = db.IsPlaintextTransportAllowed(ctx, "imap", "mail.test", 143)
	if err != nil || allowed {
		t.Fatalf("other port IsPlaintextTransportAllowed() = %v, %v; want false", allowed, err)
	}

	if _, err := db.Write().ExecContext(ctx, `
		INSERT INTO users (id, username, username_normalized, name, is_admin) VALUES ('owner', 'owner', 'owner', 'Owner', 0);
		INSERT INTO accounts (
			id, user_id, provider, email_address, imap_host, imap_port, imap_tls_mode,
			smtp_host, smtp_port, smtp_tls_mode, username, auth_method
		) VALUES (
			'account', 'owner', 'imap', 'owner@example.com', 'mail.test', 1143, 'plaintext',
			'mail.test', 2465, 'tls', 'owner@example.com', 'plain'
		)`); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	items, err := db.ListMailSecurityExceptions(ctx)
	if err != nil {
		t.Fatalf("ListMailSecurityExceptions() error = %v", err)
	}
	var plaintextID string
	for _, item := range items {
		if item.Kind != "plaintext_transport" {
			continue
		}
		plaintextID = item.ID
		if len(item.Accounts) != 1 || item.Accounts[0].ID != "account" {
			t.Fatalf("plaintext exception accounts = %#v", item.Accounts)
		}
	}
	if plaintextID == "" {
		t.Fatal("plaintext exception not listed")
	}
	if err := db.DeleteMailSecurityException(ctx, plaintextID); err != nil {
		t.Fatalf("DeleteMailSecurityException() error = %v", err)
	}
	allowed, err = db.IsPlaintextTransportAllowed(ctx, "imap", "mail.test", 1143)
	if err != nil || allowed {
		t.Fatalf("revoked IsPlaintextTransportAllowed() = %v, %v; want false", allowed, err)
	}
}

func TestPrivateTargetExceptionsAreExact(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)

	if err := db.AddPrivateTargetException(ctx, "HTTPS", "127.0.0.1", 8443, "admin"); err != nil {
		t.Fatalf("AddPrivateTargetException() error = %v", err)
	}
	allowed, err := db.IsPrivateTargetAllowed(ctx, "https", "127.0.0.1", 8443)
	if err != nil || !allowed {
		t.Fatalf("IsPrivateTargetAllowed() = %v, %v; want true", allowed, err)
	}
	allowed, err = db.IsPrivateTargetAllowed(ctx, "https", "127.0.0.1", 443)
	if err != nil || allowed {
		t.Fatalf("other private target = %v, %v; want false", allowed, err)
	}
	allowed, err = db.IsPrivateTargetAllowed(ctx, "http", "127.0.0.1", 8443)
	if err != nil || allowed {
		t.Fatalf("other protocol private target = %v, %v; want false", allowed, err)
	}

	items, err := db.ListMailSecurityExceptions(ctx)
	if err != nil {
		t.Fatalf("ListMailSecurityExceptions() error = %v", err)
	}
	if len(items) != 1 || items[0].Kind != "private_target" || items[0].Protocol != "https" || items[0].Host != "127.0.0.1" || items[0].Port != 8443 {
		t.Fatalf("private target exceptions = %#v", items)
	}
}

func TestMigrateV67ExpandsPrivateTargetExceptions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gofer.db")
	raw, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB() error = %v", err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP);
		INSERT INTO schema_version (version) VALUES (67);
		CREATE TABLE mail_security_exceptions (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL CHECK (kind IN ('http_discovery', 'plaintext_transport')),
			protocol TEXT NOT NULL DEFAULT '',
			host TEXT NOT NULL,
			port INTEGER NOT NULL DEFAULT 0,
			created_by TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(kind, protocol, host, port)
		);
		CREATE INDEX idx_mail_security_exceptions_lookup ON mail_security_exceptions(kind, protocol, host, port);
		INSERT INTO mail_security_exceptions (id, kind, host, created_by) VALUES ('legacy', 'http_discovery', 'lab.example.test', 'admin');`); err != nil {
		raw.Close()
		t.Fatalf("seed v67 schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw DB: %v", err)
	}

	db, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() migration error = %v", err)
	}
	defer db.Close()
	var version int
	if err := db.Read().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != CurrentSchemaVersion {
		t.Fatalf("migrated schema version = %d, %v; want %d", version, err, CurrentSchemaVersion)
	}
	if err := db.AddPrivateTargetException(context.Background(), "http", "127.0.0.1", 8080, "admin"); err != nil {
		t.Fatalf("AddPrivateTargetException() after migration error = %v", err)
	}
	allowed, err := db.IsPrivateTargetAllowed(context.Background(), "http", "127.0.0.1", 8080)
	if err != nil || !allowed {
		t.Fatalf("private target after migration = %v, %v; want true", allowed, err)
	}
}

func TestPlaintextTransportExceptionAcceptsHTTP(t *testing.T) {
	ctx := context.Background()
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.AddPlaintextTransportException(ctx, "HTTP", "JMAP.Lab.Test.", 8080, "admin"); err != nil {
		t.Fatalf("AddPlaintextTransportException(http) error = %v", err)
	}
	if allowed, err := db.IsPlaintextTransportAllowed(ctx, "http", "jmap.lab.test", 8080); err != nil || !allowed {
		t.Fatalf("IsPlaintextTransportAllowed(http) = %v, %v; want true", allowed, err)
	}
	if allowed, err := db.IsPlaintextTransportAllowed(ctx, "imap", "jmap.lab.test", 8080); err != nil || allowed {
		t.Fatalf("HTTP exception allowed IMAP = %v, %v", allowed, err)
	}
	if err := db.AddPlaintextTransportException(ctx, "https", "jmap.lab.test", 8443, "admin"); err == nil {
		t.Fatal("plaintext exception accepted https")
	}
}

// Exceptions made before schema 115 survive the table rebuild, and the
// rebuilt table accepts HTTP plaintext exceptions.
func TestMigrateV115KeepsExceptionsAndAllowsHTTPPlaintext(t *testing.T) {
	ctx := context.Background()
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Write().Exec(`DROP TABLE mail_security_exceptions;
CREATE TABLE mail_security_exceptions (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK (kind IN ('http_discovery', 'plaintext_transport', 'private_target')),
	protocol TEXT NOT NULL DEFAULT '',
	host TEXT NOT NULL CHECK (host <> ''),
	port INTEGER NOT NULL DEFAULT 0,
	created_by TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	CHECK (
		(kind = 'http_discovery' AND protocol = '' AND port = 0)
		OR
		(kind = 'plaintext_transport' AND protocol IN ('imap', 'smtp') AND port BETWEEN 1 AND 65535)
		OR
		(kind = 'private_target' AND protocol IN ('http', 'https', 'imap', 'smtp') AND port BETWEEN 1 AND 65535)
	),
	UNIQUE(kind, protocol, host, port)
);
INSERT INTO mail_security_exceptions (id, kind, protocol, host, port, created_by) VALUES ('old-imap', 'plaintext_transport', 'imap', 'mail.lab.test', 143, 'admin');
DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(114);`); err != nil {
		t.Fatal(err)
	}
	if err := db.AddPlaintextTransportException(ctx, "http", "jmap.lab.test", 8080, "admin"); err == nil {
		t.Fatal("old table accepted an HTTP plaintext exception")
	}
	if err := migrateV114ToV115(db.Write()); err != nil {
		t.Fatal(err)
	}
	if allowed, err := db.IsPlaintextTransportAllowed(ctx, "imap", "mail.lab.test", 143); err != nil || !allowed {
		t.Fatalf("old exception after migration = %v, %v", allowed, err)
	}
	if err := db.AddPlaintextTransportException(ctx, "http", "jmap.lab.test", 8080, "admin"); err != nil {
		t.Fatalf("HTTP plaintext exception after migration: %v", err)
	}
}

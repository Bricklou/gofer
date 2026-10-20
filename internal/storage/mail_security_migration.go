package storage

import "database/sql"

// migrateV114ToV115 lets plaintext exceptions name HTTP, for JMAP servers
// reached without TLS. SQLite cannot change a CHECK constraint in place, so
// the table is rebuilt with its rows.
func migrateV114ToV115(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version >= 115 {
		return nil
	}
	exists, err := tableExistsTx(tx, "mail_security_exceptions")
	if err != nil {
		return err
	}
	if exists {
		for _, statement := range []string{
			`ALTER TABLE mail_security_exceptions RENAME TO mail_security_exceptions_v114`,
			`DROP INDEX IF EXISTS idx_mail_security_exceptions_lookup`,
			mailSecurityExceptionsTableSQL,
			`INSERT INTO mail_security_exceptions (id, kind, protocol, host, port, created_by, created_at)
			 SELECT id, kind, protocol, host, port, created_by, created_at FROM mail_security_exceptions_v114`,
			`DROP TABLE mail_security_exceptions_v114`,
			`CREATE INDEX IF NOT EXISTS idx_mail_security_exceptions_lookup
			 ON mail_security_exceptions(kind, protocol, host, port)`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	if err := markSchemaVersion(tx, 115); err != nil {
		return err
	}
	return tx.Commit()
}

// mailSecurityExceptionsTableSQL matches the table in schema.sql.
const mailSecurityExceptionsTableSQL = `CREATE TABLE mail_security_exceptions (
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
		(kind = 'plaintext_transport' AND protocol IN ('http', 'imap', 'smtp') AND port BETWEEN 1 AND 65535)
		OR
		(kind = 'private_target' AND protocol IN ('http', 'https', 'imap', 'smtp') AND port BETWEEN 1 AND 65535)
	),
	UNIQUE(kind, protocol, host, port)
)`

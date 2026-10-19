package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

// The rebuild keeps queued outgoing mail and the rows that point at it: a
// pending calendar reply and a provider send receipt. Dropping the old table
// alone would have cascaded them away.
func TestMigrateV117KeepsOutgoingMailAndWhatReferencesIt(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	oldOutgoing := strings.Replace(outgoingSendsTableSQL, "'outlook', 'jmap'", "'outlook'", 1)
	oldReceipts := strings.Replace(providerSendReceiptSchema, "'outlook','jmap'", "'outlook'", 1)
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','gmail','robin@example.test');
INSERT INTO calendar_sources(id,user_id,account_id,provider,remote_id) VALUES('source','owner','account','gmail','primary');
DROP TABLE calendar_reply_jobs; DROP TABLE outgoing_sends;
` + oldOutgoing + `;
` + oldReceipts + `;
CREATE TABLE IF NOT EXISTS calendar_reply_jobs (
    id TEXT PRIMARY KEY REFERENCES outgoing_sends(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL REFERENCES calendar_sources(id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL, remote_id TEXT NOT NULL, version TEXT NOT NULL,
    response TEXT NOT NULL CHECK(response IN ('accepted','tentative','declined')),
    payload TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','complete','conflict','canceled','dismissed')),
    attempted_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO outgoing_sends(id,account_id,transport,envelope_from,send_after,mime_data,attempt_count,status)
 VALUES('reply','account','gmail','robin@example.test','2026-03-02 09:00:00',x'4d494d45',1,'pending'),
       ('sent','account','smtp','robin@example.test','2026-03-01 09:00:00',x'4d494d45',1,'sent');
INSERT INTO calendar_reply_jobs(id,user_id,source_id,resource_id,remote_id,version,response,payload) VALUES('reply','owner','source','event','remote','v1','accepted','{}');
INSERT INTO gofer_provider_send_receipts(send_id,provider,mailbox_subject,provider_message_id) VALUES('reply','gmail','subject','m1');
DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(116);`); err != nil {
		t.Fatal(err)
	}
	insertJMAP := `INSERT INTO outgoing_sends(id,account_id,transport,envelope_from,send_after) VALUES(?, 'account', 'jmap', 'robin@example.test', '2026-03-03 09:00:00')`
	if _, err := db.Write().Exec(insertJMAP, "jmap-before"); err == nil {
		t.Fatal("old table accepted JMAP outgoing mail")
	}
	for range 2 {
		if err := migrateV116ToV117(db.Write()); err != nil {
			t.Fatal(err)
		}
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.Read().QueryRow(query).Scan(&n); err != nil {
			t.Fatal(query, err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM outgoing_sends`); n != 2 {
		t.Fatalf("outgoing mail after migration = %d, want 2", n)
	}
	var transport, status string
	var mime []byte
	var attempts int
	if err := db.Read().QueryRow(`SELECT transport,status,mime_data,attempt_count FROM outgoing_sends WHERE id='reply'`).Scan(&transport, &status, &mime, &attempts); err != nil || transport != "gmail" || status != "pending" || string(mime) != "MIME" || attempts != 1 {
		t.Fatalf("queued send = %s %s %q %d, %v", transport, status, mime, attempts, err)
	}
	if n := count(`SELECT COUNT(*) FROM calendar_reply_jobs WHERE id='reply' AND state='pending'`); n != 1 {
		t.Fatal("pending calendar reply lost in the rebuild")
	}
	if n := count(`SELECT COUNT(*) FROM gofer_provider_send_receipts WHERE send_id='reply' AND provider_message_id='m1'`); n != 1 {
		t.Fatal("send receipt lost in the rebuild")
	}
	if n := count(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name LIKE 'idx_outgoing_sends_%'`); n != 3 {
		t.Fatalf("outgoing indexes after migration = %d, want 3", n)
	}
	if _, err := db.Write().Exec(insertJMAP, "jmap-after"); err != nil {
		t.Fatalf("JMAP outgoing mail after migration: %v", err)
	}
	// The references still work: removing a send still removes its reply.
	if _, err := db.Write().Exec(`DELETE FROM outgoing_sends WHERE id='reply'`); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT COUNT(*) FROM calendar_reply_jobs`); n != 0 {
		t.Fatal("calendar reply no longer follows its send")
	}
}

// The per-user layout step lets send receipts name JMAP, keeping their rows,
// and leaves an already updated table alone.
func TestLayoutStepAllowsJMAPSendReceipts(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	oldReceipts := strings.Replace(providerSendReceiptSchema, "'outlook','jmap'", "'outlook'", 1)
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','gmail','robin@example.test');
INSERT INTO outgoing_sends(id,account_id,transport,envelope_from,send_after) VALUES('s1','account','gmail','robin@example.test','2026-03-02 09:00:00'),('s2','account','jmap','robin@example.test','2026-03-02 09:00:00');
` + oldReceipts + `;
INSERT INTO gofer_provider_send_receipts(send_id,provider,mailbox_subject) VALUES('s1','gmail','subject');`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		tx, err := db.Write().Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := allowJMAPProviderSendReceiptsTx(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM gofer_provider_send_receipts WHERE send_id='s1'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("existing receipt after the step = %d, %v", rows, err)
	}
	if _, err := db.Write().Exec(`INSERT INTO gofer_provider_send_receipts(send_id,provider,mailbox_subject) VALUES('s2','jmap','')`); err != nil {
		t.Fatalf("JMAP receipt after the step: %v", err)
	}
}

// Databases upgraded through older versions added two attempt times as
// nullable columns; their NULLs get the column's meaning instead of failing
// the rebuild.
func TestMigrateV117FillsNullAttemptTimes(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	upgraded := strings.NewReplacer(
		"transport TEXT NOT NULL CHECK (transport IN ('smtp', 'gmail', 'outlook', 'jmap'))", "transport TEXT NOT NULL CHECK (transport IN ('smtp', 'gmail', 'outlook'))",
		"next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,\n\tis_scheduled", "next_attempt_at DATETIME,\n\tis_scheduled",
		"sent_copy_next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP", "sent_copy_next_attempt_at DATETIME",
	).Replace(outgoingSendsTableSQL)
	if !strings.Contains(upgraded, "sent_copy_next_attempt_at DATETIME,") || !strings.Contains(upgraded, "next_attempt_at DATETIME,\n\tis_scheduled") {
		t.Fatal("test table still requires the attempt times")
	}
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','imap','robin@example.test');
DROP TABLE calendar_reply_jobs; DROP TABLE outgoing_sends;
` + upgraded + `;
INSERT INTO outgoing_sends(id,account_id,transport,envelope_from,send_after,next_attempt_at,sent_copy_next_attempt_at)
 VALUES('old','account','smtp','robin@example.test','2026-03-02 09:00:00',NULL,NULL);
DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(116);`); err != nil {
		t.Fatal(err)
	}
	if err := migrateV116ToV117(db.Write()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var next, sentCopyNext string
	if err := db.Read().QueryRow(`SELECT next_attempt_at, sent_copy_next_attempt_at FROM outgoing_sends WHERE id='old'`).Scan(&next, &sentCopyNext); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(next, "2026-03-02") || sentCopyNext == "" {
		t.Fatalf("attempt times = %q, %q; want send_after and now", next, sentCopyNext)
	}
}

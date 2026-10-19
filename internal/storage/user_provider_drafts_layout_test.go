package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// The per-user layout step lets provider drafts name JMAP. It keeps queued
// drafts and their operations, ids included, and leaves an already updated
// table alone.
func TestLayoutStepAllowsJMAPProviderDrafts(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	run := func(step func(context.Context, *sql.Tx) error) {
		t.Helper()
		tx, err := db.Write().Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := step(t.Context(), tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	run(ensureUserProviderDraftSchema)
	// Turn the tables back into their earlier, Gmail and Outlook only shape.
	var states string
	if err := db.Read().QueryRow(`SELECT sql FROM sqlite_schema WHERE name='gofer_provider_draft_states'`).Scan(&states); err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(states, "'outlook','jmap'", "'outlook'", 1)
	if old == states {
		t.Fatal("test could not make the earlier table")
	}
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
INSERT INTO accounts(id,user_id,provider,email_address) VALUES('gmail-account','owner','gmail','robin@example.test'),('jmap-account','owner','jmap','sam@example.test');
DROP TABLE gofer_provider_draft_operations; DROP TABLE gofer_provider_draft_states;
` + old + `;`); err != nil {
		t.Fatal(err)
	}
	run(ensureUserProviderDraftSchema) // recreates the operations table only
	if _, err := db.Write().Exec(`INSERT INTO gofer_provider_draft_states(account_id,draft_key,provider,mailbox_subject,folder_id,local_revision) VALUES('gmail-account','<notes@example.test>','gmail','subject','drafts','r1');
INSERT INTO gofer_provider_draft_operations(id,account_id,draft_key,kind,revision_token,mime_data) VALUES(7,'gmail-account','<notes@example.test>','upsert','r1',x'00');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Write().Exec(`INSERT INTO gofer_provider_draft_states(account_id,draft_key,provider,mailbox_subject,folder_id) VALUES('jmap-account','<x@example.test>','jmap','','drafts')`); err == nil {
		t.Fatal("earlier table accepted a JMAP draft")
	}

	run(allowJMAPProviderDraftsTx)
	run(allowJMAPProviderDraftsTx)

	var revision, kind string
	if err := db.Read().QueryRow(`SELECT s.local_revision,o.kind FROM gofer_provider_draft_operations o JOIN gofer_provider_draft_states s USING(account_id,draft_key) WHERE o.id=7`).Scan(&revision, &kind); err != nil || revision != "r1" || kind != "upsert" {
		t.Fatalf("queued Gmail draft after the step = %q %q, %v", revision, kind, err)
	}
	if _, err := db.Write().Exec(`INSERT INTO gofer_provider_draft_states(account_id,draft_key,provider,mailbox_subject,folder_id) VALUES('jmap-account','<x@example.test>','jmap','','drafts');
INSERT INTO gofer_provider_draft_operations(account_id,draft_key,kind) VALUES('jmap-account','<x@example.test>','delete');`); err != nil {
		t.Fatalf("JMAP draft after the step: %v", err)
	}
	// Removing a draft's state still removes its operations.
	if _, err := db.Write().Exec(`DELETE FROM gofer_provider_draft_states WHERE account_id='jmap-account'`); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM gofer_provider_draft_operations WHERE account_id='jmap-account'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("operations left after removing their draft = %d, %v", left, err)
	}
}

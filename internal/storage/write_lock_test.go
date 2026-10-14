package storage

import (
	"path/filepath"
	"testing"
	"time"
)

// A write transaction that starts with a read must not fail with "database
// is locked" because another connection wrote to the file meanwhile: writing
// connections take the write lock when the transaction begins, so the other
// writer waits instead.
func TestWriteTransactionThatReadsFirstOutlastsAnotherWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.db")
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.Write().Exec(`INSERT INTO users(id, username, username_normalized) VALUES ('ana', 'ana', 'ana'), ('ben', 'ben', 'ben')`); err != nil {
		t.Fatal(err)
	}

	tx, err := first.Write().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM users`).Scan(&count); err != nil {
		t.Fatal(err)
	}

	otherDone := make(chan error, 1)
	go func() {
		_, err := second.Write().Exec(`UPDATE users SET name = 'Ben' WHERE id = 'ben'`)
		otherDone <- err
	}()
	select {
	case err := <-otherDone:
		t.Logf("other writer finished first: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	if _, err := tx.Exec(`UPDATE users SET name = 'Ana' WHERE id = 'ana'`); err != nil {
		t.Fatalf("write after the other connection's write: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatalf("other writer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("other writer still waiting after the transaction committed")
	}
}

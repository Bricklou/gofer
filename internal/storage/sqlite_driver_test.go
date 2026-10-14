package storage

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlowQueryLogTimesStatementsAndTransactions(t *testing.T) {
	SetSlowQueryThreshold(time.Nanosecond)
	t.Cleanup(func() { SetSlowQueryThreshold(0) })
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })

	db, err := New(filepath.Join(t.TempDir(), "timed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	output.Reset()

	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO remote_content_senders(sender_email) VALUES (?)`, "hidden@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Read().QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_content_senders WHERE sender_email = ?`, "hidden@example.test").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}

	logged := output.String()
	for _, want := range []string{
		"storage: slow statement",
		"storage: slow transaction",
		"storage: slow query",
		"INSERT INTO remote_content_senders",
		"SELECT COUNT(*) FROM remote_content_senders",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log does not contain %q:\n%s", want, logged)
		}
	}
	if strings.Contains(logged, "hidden@example.test") {
		t.Errorf("log contains bound arguments:\n%s", logged)
	}
}

func TestSlowQueryLogIsOffByDefault(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	db, err := New(filepath.Join(t.TempDir(), "quiet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	output.Reset()
	if _, err := db.Write().Exec(`INSERT INTO remote_content_senders(sender_email) VALUES ('quiet@example.test')`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "storage: slow") {
		t.Fatalf("logged with the threshold off:\n%s", output.String())
	}
}

// The same statement run again while its rows are still open on the same
// connection, as in a transaction that reads and writes, must not disturb
// either use.
func TestStatementCacheHandlesReuseWhileRowsAreOpen(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	tx, err := db.Write().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 5; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO remote_content_senders(sender_email) VALUES (?)`, fmt.Sprintf("p%d@example.test", i)); err != nil {
			t.Fatal(err)
		}
	}
	const query = `SELECT sender_email FROM remote_content_senders WHERE sender_email >= ? ORDER BY sender_email`
	outer, err := tx.QueryContext(ctx, query, "p0")
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for outer.Next() {
		var email string
		if err := outer.Scan(&email); err != nil {
			t.Fatal(err)
		}
		// Same text, different argument, while outer is mid-iteration.
		var inner int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+query+`)`, email).Scan(&inner); err != nil {
			t.Fatal(err)
		}
		innerRows, err := tx.QueryContext(ctx, query, email)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for innerRows.Next() {
			count++
		}
		innerRows.Close()
		if count != inner {
			t.Fatalf("inner query after %s returned %d rows, want %d", email, count, inner)
		}
		seen = append(seen, email)
	}
	if err := outer.Err(); err != nil {
		t.Fatal(err)
	}
	outer.Close()
	if len(seen) != 5 || seen[0] != "p0@example.test" || seen[4] != "p4@example.test" {
		t.Fatalf("outer query disturbed: %v", seen)
	}
	// After the rows are closed the cached statement is reusable.
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+query+`)`, "p3").Scan(&count); err != nil || count != 2 {
		t.Fatalf("reuse after close: %d, %v", count, err)
	}
}

func TestStatementCacheEvictsBeyondItsSize(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "evict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	conn, err := db.Write().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Distinct statement texts beyond the cache size, interleaved with an
	// open cursor whose statement gets evicted while busy.
	rows, err := conn.QueryContext(ctx, `SELECT 1 UNION ALL SELECT 2`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < statementCacheSize+20; i++ {
		var v int
		if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT %d`, i)).Scan(&v); err != nil || v != i {
			t.Fatalf("statement %d: %d, %v", i, v, err)
		}
	}
	values := 0
	for rows.Next() {
		values++
	}
	if err := rows.Close(); err != nil || values != 2 {
		t.Fatalf("evicted busy cursor returned %d rows, %v", values, err)
	}
}

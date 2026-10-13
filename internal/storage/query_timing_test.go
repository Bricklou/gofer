package storage

import (
	"bytes"
	"context"
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
	if got := sqliteDriverName(); got != "sqlite" {
		t.Fatalf("driver = %q, want the plain driver while the log is off", got)
	}
}

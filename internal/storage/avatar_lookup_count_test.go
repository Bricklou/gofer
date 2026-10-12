package storage

import (
	"context"
	"testing"
	"time"
)

// A lookup logs a row for each source it tried. Only lookups that found
// nothing count against a sender's retries, once each.
func TestFailedSenderAvatarLookupsCountLookupsNotRows(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	hash := "0bc83cb571cd1c50ba6f3e8a78ef1346"
	insert := func(at time.Time, statuses ...string) {
		t.Helper()
		for i, status := range statuses {
			provider := []string{"gravatar", "libravatar", "bimi", "domain_icon"}[i]
			if _, err := db.Write().ExecContext(ctx, `INSERT INTO avatar_attempt_logs (email_hash, email, provider, status, message, created_at) VALUES (?, 'news@brand.example', ?, ?, '', ?)`,
				hash, provider, status, at.UTC().Format("2006-01-02 15:04:05")); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now()
	insert(now.Add(-3*time.Hour), "skipped", "skipped", "missing", "found")   // found: doesn't count
	insert(now.Add(-2*time.Hour), "missing", "missing", "missing", "missing") // one failed lookup
	insert(now.Add(-1*time.Hour), "missing", "error", "missing", "missing")   // another
	insert(now.Add(-30*24*time.Hour), "missing", "missing", "missing", "missing")

	count, err := db.CountFailedSenderAvatarLookupsSince(ctx, hash, now.Add(-7*24*time.Hour))
	if err != nil || count != 2 {
		t.Fatalf("failed lookups = %d, %v; want 2", count, err)
	}
}

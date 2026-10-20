package storage

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	avatarresolver "github.com/cristianadrielbraun/gofer/internal/avatar"
	"github.com/cristianadrielbraun/gofer/internal/models"
)

// newAvatarListFixture stores one message from each kind of sender, each
// addressed to the same recipient, and the avatar records for them.
func newAvatarListFixture(t *testing.T) (*DB, map[string]string) {
	t.Helper()
	db := newFolderThreadTestDB(t)
	senders := map[string]string{
		"found":    "found@example.test",
		"expired":  "expired@example.test",
		"no-image": "no-image@example.test",
		"missing":  "missing@example.test",
		"unknown":  "never-checked@example.test",
	}
	future := time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	past := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	rows := []struct {
		kind, source, status, path, expires string
	}{
		{"found", "gravatar", "found", "/avatars/found.png", future},
		{"expired", "bimi", "found", "/avatars/expired.png", past},
		{"no-image", "gravatar", "found", "", future},
		{"missing", "gravatar", "missing", "", future},
	}
	for _, r := range rows {
		email := senders[r.kind]
		if _, err := db.Write().Exec(`INSERT INTO sender_avatars(email_hash, email, source, status, storage_path, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			avatarresolver.GravatarHash(email), email, r.source, r.status, r.path, r.expires); err != nil {
			t.Fatal(err)
		}
	}
	id := 1
	for kind, email := range senders {
		if _, err := db.Write().Exec(`INSERT INTO messages(id, account_id, subject, from_name, from_email, date_received)
			VALUES (?, 'acct', ?, ?, ?, datetime('2024-01-01', '+' || ? || ' minutes'))`, id, "From "+kind, kind, email, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Write().Exec(`INSERT INTO message_folder_state(message_id, folder_id) VALUES (?, 'box-inbox')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Write().Exec(`INSERT INTO message_recipients(message_id, kind, name, email) VALUES (?, 'to', 'Found', 'found@example.test')`, id); err != nil {
			t.Fatal(err)
		}
		id++
	}
	return db, senders
}

func TestMailListAvatarsMatchEachAvatarState(t *testing.T) {
	db, senders := newAvatarListFixture(t)
	page, err := db.GetEmailsRange(context.Background(), "box-inbox", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Emails) != len(senders) {
		t.Fatalf("listed %d messages, want %d", len(page.Emails), len(senders))
	}
	byKind := map[string]models.Contact{}
	for _, email := range page.Emails {
		byKind[strings.TrimPrefix(email.Subject, "From ")] = email.From
		if len(email.To) != 1 || email.To[0].AvatarStatus != "found" || email.To[0].AvatarURL == "" {
			t.Fatalf("recipient avatar not filled: %+v", email.To)
		}
	}
	want := map[string]struct {
		status, source string
		url            bool
	}{
		"found":    {"found", "gravatar", true},
		"expired":  {"found", "bimi", false},
		"no-image": {"found", "gravatar", false},
		"missing":  {"missing", "gravatar", false},
		"unknown":  {"unknown", "", false},
	}
	for kind, w := range want {
		got := byKind[kind]
		if got.AvatarStatus != w.status || got.AvatarSource != w.source || (got.AvatarURL != "") != w.url {
			t.Errorf("%s sender: status %q source %q url %q, want %q %q url=%v", kind, got.AvatarStatus, got.AvatarSource, got.AvatarURL, w.status, w.source, w.url)
		}
	}
}

// A list page looks avatars up once for its senders and once for its
// recipients, however many messages it shows.
func TestMailListLooksUpAvatarsOncePerPage(t *testing.T) {
	db, _ := newAvatarListFixture(t)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	SetSlowQueryThreshold(time.Nanosecond)
	t.Cleanup(func() { SetSlowQueryThreshold(0); log.SetOutput(previous) })
	if _, err := db.GetEmailsRange(context.Background(), "box-inbox", 0, 50); err != nil {
		t.Fatal(err)
	}
	if lookups := strings.Count(output.String(), "FROM sender_avatars"); lookups != 2 {
		t.Fatalf("avatar lookups for one page = %d, want 2:\n%s", lookups, output.String())
	}
}

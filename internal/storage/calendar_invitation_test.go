package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestCalendarInvitationMessagesInOrderOnce(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "invitations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
 INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','imap','owner@example.com'),('mail-only','owner','imap','other@example.com');
 INSERT INTO folders(id,account_id,name,role) VALUES('inbox','account','Inbox','inbox'),('junk','account','Junk','junk'),('other-inbox','mail-only','Inbox','inbox');`); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceCalendarSources(t.Context(), "owner", "account", "caldav", []CalendarSource{{ID: "source", RemoteID: "https://cal.example.com/", IsSelected: true}}); err != nil {
		t.Fatal(err)
	}
	// 3 came first; 2 is junk, 4 the account's own, 5 too old, 6 too large,
	// and 7 in an account without a CalDAV calendar.
	if _, err := db.Write().Exec(`INSERT INTO messages(id,account_id,from_email,date_received,size_bytes) VALUES
 (1,'account','host@example.com',strftime('%Y-%m-%d %H:%M:%S','now','-1 hour'),100),
 (2,'account','host@example.com',strftime('%Y-%m-%d %H:%M:%S','now','-1 hour'),100),
 (3,'account','host@example.com',strftime('%Y-%m-%d %H:%M:%S','now','-2 hours'),100),
 (4,'account','owner@example.com',strftime('%Y-%m-%d %H:%M:%S','now','-1 hour'),100),
 (5,'account','host@example.com',strftime('%Y-%m-%d %H:%M:%S','now','-8 days'),100),
 (6,'account','host@example.com',strftime('%Y-%m-%d %H:%M:%S','now','-1 hour'),5000),
 (7,'mail-only','host@example.com',strftime('%Y-%m-%d %H:%M:%S','now','-1 hour'),100);
 INSERT INTO message_folder_state(message_id,folder_id) VALUES(1,'inbox'),(2,'junk'),(3,'inbox'),(4,'inbox'),(5,'inbox'),(6,'inbox'),(7,'other-inbox');`); err != nil {
		t.Fatal(err)
	}
	list := func() []CalendarInvitationMessage {
		t.Helper()
		messages, err := db.ListCalendarInvitationMessages(t.Context(), "owner", "account", 1000, 12)
		if err != nil {
			t.Fatal(err)
		}
		return messages
	}
	messages := list()
	if len(messages) != 2 || messages[0].ID != 3 || messages[1].ID != 1 || messages[0].Retry {
		t.Fatalf("candidates: %+v", messages)
	}
	// A retry keeps its place, and isn't due until later.
	if err := db.FinishCalendarInvitationMessage(t.Context(), messages[0], "retry", "offline"); err != nil {
		t.Fatal(err)
	}
	messages = list()
	if len(messages) != 2 || messages[0].ID != 3 || !messages[0].Retry || messages[0].Due || messages[0].Attempts != 1 {
		t.Fatalf("retry: %+v", messages)
	}
	if err := db.FinishCalendarInvitationMessage(t.Context(), messages[0], "complete", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishCalendarInvitationMessage(t.Context(), messages[1], "ignored", "unsigned"); err != nil {
		t.Fatal(err)
	}
	if messages = list(); len(messages) != 0 {
		t.Fatalf("read twice: %+v", messages)
	}
}

// Mail that came before Gofer read mail for invitations is left alone.
func TestCalendarInvitationMigrationMarksRecentMail(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('owner','owner','owner');
 INSERT INTO accounts(id,user_id,provider,email_address) VALUES('account','owner','imap','owner@example.com');
 INSERT INTO messages(id,account_id,from_email,date_received) VALUES(1,'account','host@example.com',strftime('%Y-%m-%d %H:%M:%S','now','-1 hour')),(2,'account','host@example.com','2020-01-01 00:00:00');
 DROP TABLE calendar_invitation_messages; DELETE FROM schema_version; INSERT INTO schema_version(version) VALUES(112);`); err != nil {
		t.Fatal(err)
	}
	if err := migrateV112ToV113(db.Write()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.Read().QueryRow(`SELECT state FROM calendar_invitation_messages WHERE message_id=1`).Scan(&state); err != nil || state != "ignored" {
		t.Fatalf("recent mail: %q %v", state, err)
	}
	if err := db.Read().QueryRow(`SELECT state FROM calendar_invitation_messages WHERE message_id=2`).Scan(&state); err != sql.ErrNoRows {
		t.Fatalf("old mail marked: %v", err)
	}
}

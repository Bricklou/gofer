package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A sync batch records each person once, counting every message they were
// in, with the name from their newest message.
func TestSyncBatchRecordsObservedContactsOnce(t *testing.T) {
	db := newFolderThreadTestDB(t)
	ctx := context.Background()
	var added []string
	db.SetContactActivityHook(func(event ContactActivityNotification) {
		if event.EventType == "observed_contact_added" {
			added = append(added, event.Email)
		}
	})
	base := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	var batch []SyncMessage
	for i := 0; i < 6; i++ {
		batch = append(batch, SyncMessage{
			AccountID: "acct", FolderID: "box-inbox", RemoteUID: uint32(i + 1),
			MessageID: fmt.Sprintf("<batch-%d@example.test>", i), Subject: "Planning",
			FromName: fmt.Sprintf("Dana Reyes %d", i), FromEmail: "Dana@Example.test",
			DateSent:     base.Add(time.Duration(i) * time.Hour),
			ToRecipients: []Recipient{{Name: "Reader", Email: "reader@example.test"}},
		})
	}
	if err := db.UpsertSyncMessages(ctx, batch); err != nil {
		t.Fatal(err)
	}

	var name string
	var count int
	if err := db.Read().QueryRow(`SELECT observed_name, message_count FROM contact_observations
		WHERE normalized_email = 'dana@example.test'`).Scan(&name, &count); err != nil {
		t.Fatal(err)
	}
	if count != 6 || name != "Dana Reyes 5" {
		t.Fatalf("observation = %q × %d, want the newest name and 6 messages", name, count)
	}
	var observations, events int
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM contact_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := db.Read().QueryRow(`SELECT COUNT(*) FROM contact_activity_events WHERE event_type = 'observed_contact_added'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if observations != 2 || events != 2 || len(added) != 2 {
		t.Fatalf("observations %d, activity events %d, notifications %v; want one per person", observations, events, added)
	}

	// A later batch adds to the existing people without announcing them again.
	batch = batch[:1]
	batch[0].MessageID, batch[0].RemoteUID = "<batch-later@example.test>", 50
	if err := db.UpsertSyncMessages(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := db.Read().QueryRow(`SELECT message_count FROM contact_observations WHERE normalized_email = 'dana@example.test'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 7 || len(added) != 2 {
		t.Fatalf("after a second batch: count %d, notifications %v", count, added)
	}
}

package storage

import (
	"context"
	"testing"
	"time"
)

// While JMAP is turned off, the workers leave JMAP accounts' queued mail and
// changes unclaimed, so they wait without using up attempts, while other
// accounts' work goes on. Turned on again, the JMAP work is claimed.
func TestJMAPQueuesWaitWhileJMAPIsOff(t *testing.T) {
	ctx := context.Background()
	db := newContactsTestDB(t)
	if _, err := db.Write().ExecContext(ctx, `INSERT INTO accounts (id, user_id, provider, email_address) VALUES
		('smtp', 'default', 'imap', 'user@example.com'), ('jmap', 'default', 'jmap', 'robin@example.test')`); err != nil {
		t.Fatalf("insert accounts: %v", err)
	}
	other, err := db.QueueOutgoingSend(ctx, outgoingTestInput("smtp", 0, time.Now().Add(-time.Minute), false))
	if err != nil {
		t.Fatal(err)
	}
	input := outgoingTestInput("jmap", 0, time.Now().Add(-time.Minute), false)
	input.Transport, input.DraftID = OutgoingTransportJMAP, "<jmap-draft@example.test>"
	waiting, err := db.QueueOutgoingSend(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertFolders(ctx, []UpsertFolderInput{{ID: "jmap_inbox", AccountID: "jmap", RemoteID: "Inbox", ProviderRemoteID: "mb-inbox", Name: "Inbox", Role: "inbox", Selectable: true}}); err != nil {
		t.Fatal(err)
	}
	ids, err := db.UpsertProviderSyncMessages(ctx, []ProviderSyncMessage{{AccountID: "jmap", FolderID: "jmap_inbox", ProviderMessageID: "e1",
		InternetMessageID: "<e1@example.test>", Subject: "Lunch?", FromEmail: "sam@example.test", DateSent: time.Now(), DateReceived: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetMessageReadAndQueue(ctx, ids["e1"], true); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GOFER_JMAP_EXPERIMENT", "false")
	sends, err := db.ClaimDueOutgoingSends(ctx, time.Now(), 10)
	if err != nil || len(sends) != 1 || sends[0].ID != other.ID {
		t.Fatalf("claimed sends while JMAP is off = %+v, %v; want only the other account's", sends, err)
	}
	mutations, err := db.ClaimDueMessageMutations(ctx, time.Now(), 10)
	if err != nil || len(mutations) != 0 {
		t.Fatalf("claimed changes while JMAP is off = %+v, %v", mutations, err)
	}
	if got, err := db.GetOutgoingSend(ctx, waiting.ID); err != nil || got.Status != OutgoingSendPending || got.AttemptCount != 0 {
		t.Fatalf("waiting JMAP send = %+v, %v", got, err)
	}

	t.Setenv("GOFER_JMAP_EXPERIMENT", "true")
	sends, err = db.ClaimDueOutgoingSends(ctx, time.Now(), 10)
	if err != nil || len(sends) != 1 || sends[0].ID != waiting.ID {
		t.Fatalf("claimed sends with JMAP on = %+v, %v", sends, err)
	}
	mutations, err = db.ClaimDueMessageMutations(ctx, time.Now(), 10)
	if err != nil || len(mutations) != 1 || mutations[0].ProviderType != MessageMutationProviderJMAP {
		t.Fatalf("claimed changes with JMAP on = %+v, %v", mutations, err)
	}
}

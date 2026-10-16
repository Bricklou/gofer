package storage

import (
	"errors"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

func TestSaveContactWithoutEmail(t *testing.T) {
	db := newContactsTestDB(t)
	ctx := t.Context()

	if _, err := db.SaveContact(ctx, "default", models.Contact{}); !errors.Is(err, ErrContactEmpty) {
		t.Fatalf("SaveContact(empty) error = %v, want ErrContactEmpty", err)
	}
	if _, err := db.SaveContact(ctx, "default", models.Contact{Name: "Robin Vale", Email: "not an address"}); !errors.Is(err, ErrContactEmailInvalid) {
		t.Fatalf("SaveContact(invalid email) error = %v, want ErrContactEmailInvalid", err)
	}

	phoneOnly, err := db.SaveContact(ctx, "default", models.Contact{Phone: "+1 555 0142"})
	if err != nil {
		t.Fatalf("SaveContact(phone only) error = %v", err)
	}
	if phoneOnly.Name != "+1 555 0142" || phoneOnly.Email != "" || phoneOnly.Phone != "+1 555 0142" || phoneOnly.Initials != "#" {
		t.Fatalf("phone-only contact = name %q email %q phone %q initials %q", phoneOnly.Name, phoneOnly.Email, phoneOnly.Phone, phoneOnly.Initials)
	}
	named, err := db.SaveContact(ctx, "default", models.Contact{Name: "Robin Vale", Phone: "+1 555 0142"})
	if err != nil {
		t.Fatalf("SaveContact(name and phone) error = %v", err)
	}
	if named.ID == phoneOnly.ID {
		t.Fatal("contacts without email were merged by phone number")
	}
	if named.Initials != "RV" {
		t.Fatalf("Initials = %q, want RV", named.Initials)
	}

	// The contact list finds them by phone number and shows it, while address
	// suggestions only offer contacts that have an email address.
	listed, err := db.ListContacts(ctx, "default", models.ContactFilters{Query: "555 0142"}, 10, 0)
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListContacts(phone) = %d contacts, %v; want 2", len(listed), err)
	}
	for _, contact := range listed {
		if contact.Phone != "+1 555 0142" {
			t.Fatalf("listed contact phone = %q", contact.Phone)
		}
	}
	if _, err := db.SaveContact(ctx, "default", models.Contact{Name: "Robin Ash", Email: "robin@icloud.com"}); err != nil {
		t.Fatal(err)
	}
	suggested, err := db.SearchContacts(ctx, "default", "robin", 10)
	if err != nil || len(suggested) != 1 || suggested[0].Email != "robin@icloud.com" {
		t.Fatalf("SearchContacts() = %#v, %v; want only the contact with an email address", suggested, err)
	}

	// An email address can be added later, and removed again.
	named.Email = "robin.vale@example.org"
	updated, err := db.SaveContact(ctx, "default", named)
	if err != nil || updated.ID != named.ID || updated.Email != "robin.vale@example.org" {
		t.Fatalf("adding an email = %#v, %v", updated, err)
	}
	updated.Email = ""
	cleared, err := db.SaveContact(ctx, "default", updated)
	if err != nil || cleared.ID != named.ID || cleared.Email != "" || cleared.Name != "Robin Vale" {
		t.Fatalf("removing the email = %#v, %v", cleared, err)
	}
}

func TestUserContactEditAndGoferSyncWithoutEmail(t *testing.T) {
	db := newInboundContactFixture(t)
	request := models.Contact{Name: "Robin Vale", Phone: "+1 555 0142", GoferSyncEnabled: true, SaveTargets: []string{"local", "account:target"}}
	snapshot, err := db.SnapshotUserContactEdit(t.Context(), "default", request, inboundTestGuard, nil)
	if err != nil {
		t.Fatalf("SnapshotUserContactEdit() error = %v", err)
	}
	result, err := db.SaveUserContactEdit(t.Context(), snapshot, false, inboundTestGuard)
	if err != nil || result.Contact.ID == "" || result.OperationID == "" {
		t.Fatalf("SaveUserContactEdit() = %#v, %v; want a saved contact with queued sync", result, err)
	}
	claim := contactSyncClaimFor(t, db, "default")
	p := contactSyncProfileFor(t, db, claim)
	if p.Contact().Phone != "+1 555 0142" || p.Contact().Email != "" || len(p.Targets()) != 1 || p.Targets()[0].AccountID != "target" {
		t.Fatalf("sync profile = %#v targets %#v", p.Contact(), p.Targets())
	}

	if _, err := db.SnapshotUserContactEdit(t.Context(), "default", models.Contact{Name: "Robin Vale", Email: "robin"}, inboundTestGuard, nil); !errors.Is(err, ErrContactEmailInvalid) {
		t.Fatalf("invalid email error = %v, want ErrContactEmailInvalid", err)
	}

	// Setting up Gofer Sync for a provider contact without an email.
	inbound := publishTestContact(t, db, InboundContact{Contact: models.Contact{Name: "Sam Reed", Phone: "+1 555 0199"}, RemoteID: "people/sam"})
	if err := db.InitializeContactCanonicalFields(t.Context(), "default", inbound.ProfileID, nil); err != nil {
		t.Fatalf("InitializeContactCanonicalFields() error = %v", err)
	}
}

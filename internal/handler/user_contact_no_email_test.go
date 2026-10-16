package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/config"
	"github.com/cristianadrielbraun/gofer/internal/mail"
	"github.com/cristianadrielbraun/gofer/internal/mailauth"
	"github.com/cristianadrielbraun/gofer/internal/models"
	"github.com/cristianadrielbraun/gofer/internal/netguard"
	"github.com/cristianadrielbraun/gofer/internal/providers"
	"github.com/cristianadrielbraun/gofer/internal/storage"
	"github.com/cristianadrielbraun/gofer/internal/store"
	"golang.org/x/oauth2"
)

// A contact without an email address has no existing Google copy to look up
// by address, so Gofer Sync creates one without searching first.
func TestUserContactPushGoogleWithoutEmailCreatesWithoutSearching(t *testing.T) {
	var creates, updates, searches int
	var mu sync.Mutex
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("wrong token")
			http.Error(w, "wrong token", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/people:searchContacts":
			searches++
			_, _ = w.Write([]byte(`{"results":[]}`))
		case "/people:createContact":
			creates++
			var person googlePerson
			if err := json.NewDecoder(r.Body).Decode(&person); err != nil {
				t.Error(err)
			}
			if len(person.EmailAddresses) != 0 || len(person.PhoneNumbers) != 1 || person.PhoneNumbers[0].Value != "+1 555 0142" || len(person.Names) != 1 || person.Names[0].GivenName != "Robin Vale" {
				t.Errorf("created contact %+v", person)
			}
			_, _ = w.Write([]byte(`{"resourceName":"people/created","etag":"v1"}`))
		case "/people/created:updateContact":
			updates++
			var person googlePerson
			_ = json.NewDecoder(r.Body).Decode(&person)
			if person.Etag != "v1" {
				t.Error("missing current ETag")
			}
			_, _ = w.Write([]byte(`{"resourceName":"people/created","etag":"v2"}`))
		default:
			http.Error(w, "unexpected provider path", 400)
		}
	}))
	defer api.Close()
	previous := http.DefaultTransport
	target, _ := url.Parse(api.URL)
	http.DefaultTransport = userContactPushTransport{base: previous, target: target}
	defer netguard.UseTransportForTests(http.DefaultTransport)()
	defer func() { http.DefaultTransport = previous }()
	system, err := storage.New(filepath.Join(t.TempDir(), "system.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()
	if _, err := system.Write().Exec(`INSERT INTO users(id,username,username_normalized) VALUES('alice','alice','alice')`); err != nil {
		t.Fatal(err)
	}
	stores, err := storage.NewUserStores(system, storage.UserStoreOptions{MaxOpen: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer stores.Close(context.Background())
	routing, err := storage.NewAccountRouting(stores)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	accounts, err := config.NewUserAccountStore(routing, key)
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.CreateAccount(t.Context(), "alice", providers.GmailAccountRequest("alice@example.com", "alice", "subject"))
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.WithAccountForUser(t.Context(), "alice", account.ID, func(_ *config.AccountStore, db *storage.DB) error {
		_, err := db.Write().Exec(`UPDATE accounts SET email_sync_enabled=0 WHERE id=?`, account.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	imap, err := mail.NewUserIMAP(ctx, accounts, store.NewBlobStore(t.TempDir()), mail.NewSyncOrchestrator(system, nil, nil, nil).Events())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); imap.Wait() }()
	credentials, err := mailauth.NewUserCredentials(ctx, &mailauth.Config{GoogleClient: &oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{TokenURL: api.URL + "/token"}}}, routing, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); credentials.Wait() }()
	expires := time.Now().Add(time.Hour)
	if err := credentials.UpsertForUser(ctx, "alice", account.ID, "google", "subject", "access", "refresh", "Bearer", &expires, "https://www.googleapis.com/auth/contacts"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{userStorage: routing, userAccounts: accounts, userIMAP: imap, userCredentials: credentials}
	var profile string
	seed := func() {
		if err := accounts.WithUser(ctx, "alice", func(_ *config.AccountStore, db *storage.DB) error {
			if profile == "" {
				contact, err := db.SaveContact(ctx, "alice", models.Contact{Name: "Robin Vale", Phone: "+1 555 0142", GoferSyncEnabled: true, SaveTargets: []string{"account:" + account.ID}})
				if err != nil {
					return err
				}
				profile = contact.ID
			}
			_, err := db.EnqueueContactSyncOperation(ctx, "alice", models.Contact{ID: profile, Phone: "111"}, nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed()
	if count, err := h.ProcessUserContactSyncOperations(ctx, "alice", 1); err != nil || count != 1 {
		t.Fatal("create job", count, err)
	}
	seed()
	if count, err := h.ProcessUserContactSyncOperations(ctx, "alice", 1); err != nil || count != 1 {
		t.Fatal("update job", count, err)
	}
	mu.Lock()
	gotCreates, gotUpdates, gotSearches := creates, updates, searches
	mu.Unlock()
	if gotCreates != 1 || gotUpdates != 1 || gotSearches != 0 {
		t.Fatal("wrong provider calls", gotCreates, gotUpdates, gotSearches)
	}
	if err := accounts.WithUser(ctx, "alice", func(_ *config.AccountStore, db *storage.DB) error {
		source, err := db.GetContactSource(ctx, "alice", profile, "gmail", account.ID)
		if err != nil {
			return err
		}
		if source == nil || source.Etag != "v2" {
			t.Fatal("missing acknowledged source", source)
		}
		var count int
		if err := db.Read().QueryRow(`SELECT COUNT(*) FROM contact_sync_operations WHERE status='done'`).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Fatal("jobs not completed", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestContactFromFormEmailIsOptional(t *testing.T) {
	form := url.Values{"name": {"Robin Vale"}, "phone": {"+1 555 0142"}, "avatar_action": {"preserve"}}
	r := httptest.NewRequest(http.MethodPost, "/api/contacts", nil)
	r.Form = form
	contact, err := contactFromForm(r, nil)
	if err != nil || contact.Email != "" || contact.Phone != "+1 555 0142" {
		t.Fatalf("contactFromForm(no email) = %#v, %v", contact, err)
	}

	// Only an additional address filled in: it becomes the main one.
	r.Form = url.Values{"name": {"Robin Vale"}, "additional_emails": {"robin@icloud.com", "robin@example.org"}, "additional_email_labels": {"home", "work"}}
	contact, err = contactFromForm(r, nil)
	if err != nil || contact.Email != "robin@icloud.com" || contact.EmailLabel != "home" || len(contact.AdditionalEmails) != 1 || contact.AdditionalEmails[0] != "robin@example.org" || contact.AdditionalEmailLabels[0] != "work" {
		t.Fatalf("contactFromForm(additional only) = %#v, %v", contact, err)
	}
}

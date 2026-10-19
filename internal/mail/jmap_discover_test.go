package mail

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/mail/autodiscover"
	"github.com/cristianadrielbraun/gofer/internal/mail/jmap"
	"github.com/cristianadrielbraun/gofer/internal/models"
)

// jmapMailServer answers session requests for robin@example.test; withMail
// says whether the session includes a mail account. hits counts requests.
func jmapMailServer(t *testing.T, withMail bool, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "robin@example.test" || pass != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		primary := `{}`
		if withMail {
			primary = `{"urn:ietf:params:jmap:mail": "a1"}`
		}
		_, _ = io.WriteString(w, `{"capabilities": {"urn:ietf:params:jmap:core": {}, "urn:ietf:params:jmap:mail": {}},
			"accounts": {"a1": {"name": "robin@example.test"}}, "primaryAccounts": `+primary+`,
			"username": "robin@example.test", "apiUrl": "/jmap/", "state": "s1"}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func allowJMAP(plaintext, private bool) JMAPEndpointPolicy {
	return func(context.Context, jmap.Endpoint) (bool, bool, error) { return plaintext, private, nil }
}

// A bare host is tried over HTTPS first; this server only speaks HTTP, so the
// approved plain HTTP candidate is the one found.
func TestDiscoverJMAPSessionFallsBackToApprovedHTTP(t *testing.T) {
	server := jmapMailServer(t, true, nil)
	host := strings.TrimPrefix(server.URL, "http://")
	candidates, err := autodiscover.JMAPSessionCandidates(context.Background(), "robin@example.test", host, nil)
	if err != nil {
		t.Fatal(err)
	}
	found, session, err := DiscoverJMAPSession(context.Background(), candidates, "robin@example.test", "secret", allowJMAP(true, true))
	if err != nil {
		t.Fatalf("DiscoverJMAPSession: %v", err)
	}
	if found != server.URL+"/.well-known/jmap" || session.PrimaryAccount(jmap.CapabilityMail) != "a1" {
		t.Fatalf("found %q session %+v", found, session)
	}
}

func TestDiscoverJMAPSessionNeverSendsPasswordOverUnapprovedHTTP(t *testing.T) {
	var hits atomic.Int32
	server := jmapMailServer(t, true, &hits)
	_, _, err := DiscoverJMAPSession(context.Background(), []string{server.URL + "/.well-known/jmap"}, "robin@example.test", "secret", allowJMAP(false, true))
	var failed *JMAPDiscoveryError
	if !errors.As(err, &failed) || len(failed.Attempts) != 1 || !strings.Contains(failed.Attempts[0].Reason, "HTTP plaintext exception") {
		t.Fatalf("err = %v, want a not-tried attempt", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server received %d requests", hits.Load())
	}
}

func TestDiscoverJMAPSessionExplainsBlockedPrivateAddress(t *testing.T) {
	server := jmapMailServer(t, true, nil)
	_, _, err := DiscoverJMAPSession(context.Background(), []string{server.URL + "/.well-known/jmap"}, "robin@example.test", "secret", allowJMAP(true, false))
	var failed *JMAPDiscoveryError
	if !errors.As(err, &failed) || !strings.Contains(failed.Attempts[0].Reason, "HTTP private target exception") {
		t.Fatalf("err = %v, want a private network attempt", err)
	}
}

func TestDiscoverJMAPSessionStopsAtRejectedLogin(t *testing.T) {
	var laterHits atomic.Int32
	first := jmapMailServer(t, true, nil)
	later := jmapMailServer(t, true, &laterHits)
	_, _, err := DiscoverJMAPSession(context.Background(),
		[]string{first.URL + "/.well-known/jmap", later.URL + "/.well-known/jmap"},
		"robin@example.test", "wrong", allowJMAP(true, true))
	if !errors.Is(err, ErrJMAPLoginRejected) {
		t.Fatalf("err = %v, want ErrJMAPLoginRejected", err)
	}
	if laterHits.Load() != 0 {
		t.Fatal("the password went on to the next candidate")
	}
}

func TestDiscoverJMAPSessionSkipsServerWithoutMail(t *testing.T) {
	noMail := jmapMailServer(t, false, nil)
	withMail := jmapMailServer(t, true, nil)
	found, _, err := DiscoverJMAPSession(context.Background(),
		[]string{noMail.URL + "/.well-known/jmap", withMail.URL + "/.well-known/jmap"},
		"robin@example.test", "secret", allowJMAP(true, true))
	if err != nil || found != withMail.URL+"/.well-known/jmap" {
		t.Fatalf("found %q, %v", found, err)
	}
}

// Runs against a real server when GOFER_JMAP_TEST_URL, _USER and _PASSWORD
// are set, discovering it from its host and port alone with every endpoint
// approved.
func TestDiscoverJMAPSessionLiveServer(t *testing.T) {
	sessionURL := os.Getenv("GOFER_JMAP_TEST_URL")
	user := os.Getenv("GOFER_JMAP_TEST_USER")
	password := os.Getenv("GOFER_JMAP_TEST_PASSWORD")
	if sessionURL == "" || user == "" || password == "" {
		t.Skip("GOFER_JMAP_TEST_URL, GOFER_JMAP_TEST_USER and GOFER_JMAP_TEST_PASSWORD not set")
	}
	parsed, err := url.Parse(sessionURL)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := autodiscover.JMAPSessionCandidates(context.Background(), user, parsed.Host, nil)
	if err != nil {
		t.Fatal(err)
	}
	found, session, err := DiscoverJMAPSession(context.Background(), candidates, user, password, allowJMAP(true, true))
	if err != nil {
		t.Fatalf("DiscoverJMAPSession: %v", err)
	}
	t.Logf("candidates=%v found=%s mail account=%s", candidates, found, session.PrimaryAccount(jmap.CapabilityMail))
}

// In managed mode the account's connection test signs in to its saved JMAP
// session URL, within the instance's exceptions for that server.
func TestUserTestAccountChecksJMAPSession(t *testing.T) {
	f := newUserGmailFixture(t)
	server := jmapMailServer(t, true, nil)
	sessionURL := server.URL + "/.well-known/jmap"
	if err := f.worker.accounts.UpdateAccount(t.Context(), "alice", f.ids["alice"], &models.CreateAccountRequest{
		Provider: "jmap", JMAPSessionURL: sessionURL, Username: "robin@example.test", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	test := func() models.ConnectionTestResult {
		t.Helper()
		results, err := f.worker.TestAccount(t.Context(), "alice", f.ids["alice"])
		if err != nil || len(results) != 1 || results[0].Service != "jmap" {
			t.Fatalf("TestAccount = %+v, %v", results, err)
		}
		return results[0]
	}
	if result := test(); result.Success || !strings.Contains(result.Error, "HTTP plaintext exception") {
		t.Fatalf("without exceptions: %+v", result)
	}
	endpoint, err := jmap.ParseEndpoint(sessionURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.system.AddPlaintextTransportException(t.Context(), "http", endpoint.Host, endpoint.Port, "administrator"); err != nil {
		t.Fatal(err)
	}
	if err := f.system.AddPrivateTargetException(t.Context(), "http", endpoint.Host, endpoint.Port, "administrator"); err != nil {
		t.Fatal(err)
	}
	if result := test(); !result.Success || !strings.Contains(result.Message, "mail account a1") {
		t.Fatalf("with exceptions: %+v", result)
	}
	// Other workers still refuse JMAP until it has its own sync.
	if err := f.worker.Sync(t.Context(), "alice", f.ids["alice"]); err == nil {
		t.Fatal("IMAP sync ran for a JMAP account")
	}
}

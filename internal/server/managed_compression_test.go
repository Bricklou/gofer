package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/storage"
)

func gzipRequest(f *managedMailFixture, owner, path string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8090"+path, nil)
	request.Header.Set("Origin", "http://127.0.0.1:8090")
	request.Header.Set("Accept-Encoding", "gzip")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.AddCookie(&http.Cookie{Name: "gofer_session", Value: f.tokens[owner]})
	recorder := httptest.NewRecorder()
	f.app.ServeHTTP(recorder, request)
	return recorder
}

// Pages that carry a CSRF token go out uncompressed; mail list fragments,
// which carry none, are compressed.
func TestManagedResponsesCompressOnlyWithoutTokens(t *testing.T) {
	t.Chdir("../..") // Development builds serve assets from ./assets.
	f := newManagedMailFixture(t)
	awaitManagedCondition(t, func() bool { return f.messageCount(t, "alice", "alice native private message") == 1 })

	page := gzipRequest(f, "alice", "/", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("page status %d", page.Code)
	}
	if page.Header().Get("Content-Encoding") != "" {
		t.Fatal("page with a CSRF token was compressed")
	}
	if !strings.Contains(page.Body.String(), `name="_csrf"`) {
		t.Fatal("page no longer carries the logout token this test relies on")
	}
	if !strings.Contains(page.Body.String(), `/assets/js/app.js?v=`) {
		t.Fatal("page links app.js without its version")
	}

	folder := storage.FolderIDForIdentity(f.accounts["alice"], "imap", "INBOX")
	fragment := gzipRequest(f, "alice", "/mail/folder/"+url.PathEscape(folder)+"/items?start=0&limit=50", map[string]string{"HX-Request": "true"})
	if fragment.Code != http.StatusOK || fragment.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("mail list fragment: status %d, encoding %q, type %q, %d bytes, token %v; want gzip", fragment.Code, fragment.Header().Get("Content-Encoding"), fragment.Header().Get("Content-Type"), fragment.Body.Len(), strings.Contains(fragment.Body.String(), "_csrf"))
	}
	sent := fragment.Body.Len()
	reader, err := gzip.NewReader(fragment.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("mail list fragment: %d bytes as HTML, %d bytes sent", len(body), sent)
	if !strings.Contains(string(body), "alice native private message") || strings.Contains(string(body), `name="_csrf"`) {
		t.Fatal("fragment content unexpected")
	}
}

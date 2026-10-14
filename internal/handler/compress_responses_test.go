package handler

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func compressTestRequest(t *testing.T, path, contentType, body string, flush bool) *httptest.ResponseRecorder {
	t.Helper()
	handler := CompressResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		half := len(body) / 2
		_, _ = io.WriteString(w, body[:half])
		if flush {
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, body[half:])
	}))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func gunzipBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCompressResponsesGzipsPagesAndJSON(t *testing.T) {
	body := strings.Repeat(`<div class="mail-row">A subject line and a sender</div>`, 100)
	for _, contentType := range []string{"text/html; charset=utf-8", "application/json"} {
		rec := compressTestRequest(t, "/mail/folder/inbox/items", contentType, body, false)
		if rec.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Fatalf("%s: headers %v, want gzip and Vary", contentType, rec.Header())
		}
		if rec.Body.Len() >= len(body)/4 {
			t.Errorf("%s: %d bytes compressed from %d", contentType, rec.Body.Len(), len(body))
		}
		if got := gunzipBody(t, rec); got != body {
			t.Fatalf("%s: body changed by compression", contentType)
		}
	}
}

func TestCompressResponsesLeavesOtherResponsesAlone(t *testing.T) {
	large := strings.Repeat("x", 4096)
	for _, tc := range []struct {
		name, path, contentType, body string
		flush                         bool
	}{
		{"small page", "/mail", "text/html", "<p>short</p>", false},
		{"download", "/email/1/raw", "application/octet-stream", large, false},
		{"progress stream", "/api/contacts/discover", "application/x-ndjson", large, true},
		{"security settings", "/settings/security/password", "text/html", large, false},
		{"administration", "/admin/users", "text/html", large, false},
		{"event stream", "/api/events", "text/event-stream", large, false},
	} {
		rec := compressTestRequest(t, tc.path, tc.contentType, tc.body, tc.flush)
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != tc.body {
			t.Errorf("%s: encoding %q, body intact %v; want it sent as it is", tc.name, rec.Header().Get("Content-Encoding"), rec.Body.String() == tc.body)
		}
	}

	// Browsers that do not accept gzip get plain responses.
	handler := CompressResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, large)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mail", nil))
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("compressed for a browser that did not ask")
	}
}

func TestCompressResponsesKeepsStatusAndHeaders(t *testing.T) {
	handler := CompressResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("HX-Trigger", "mail-updated")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, strings.Repeat("<p>error detail</p>", 200))
	}))
	req := httptest.NewRequest(http.MethodPost, "/compose/send", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("HX-Trigger") != "mail-updated" || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
}

// templ flushes at the end of every render; that must not stop a page from
// being compressed.
func TestCompressResponsesIgnoresTemplFinalFlush(t *testing.T) {
	body := strings.Repeat("<li>message</li>", 200)
	rec := compressTestRequest(t, "/mail", "text/html; charset=utf-8", body, true)
	if rec.Header().Get("Content-Encoding") != "gzip" || gunzipBody(t, rec) != body {
		t.Fatalf("flushed page not compressed: %v", rec.Header())
	}
}

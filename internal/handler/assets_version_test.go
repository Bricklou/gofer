package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/assetver"
)

// Pages link assets with their content version, which browsers may keep for
// a year; any other request for an asset must check again, so an upgrade
// never leaves an old script running.
func TestAssetCachingFollowsVersions(t *testing.T) {
	t.Chdir("../..") // Assets are served from ./assets in tests.
	t.Setenv("GO_ENV", "production")
	mux := http.NewServeMux()
	setupAssetsRoutes(mux)
	current := assetver.URL("/assets/js/app.js")
	if !strings.Contains(current, "?v=") {
		t.Fatalf("no version for app.js: %q", current)
	}
	version := current[strings.Index(current, "?v=")+3:]

	get := func(path string, header map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for key, value := range header {
			req.Header.Set(key, value)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(current, nil); rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("current version: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	for _, stale := range []string{"/assets/js/app.js", "/assets/js/app.js?v=0123456789ab"} {
		rec := get(stale, nil)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, no-cache" {
			t.Fatalf("%s: %d %q, want public, no-cache", stale, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
	// Rechecking an unchanged asset costs a 304, also for embedded files,
	// which carry no modification time.
	if rec := get("/assets/js/app.js", map[string]string{"If-None-Match": `"` + version + `"`}); rec.Code != http.StatusNotModified {
		t.Fatalf("unchanged asset recheck: %d, want 304", rec.Code)
	}
}

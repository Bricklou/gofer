package assetver

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestURLVersionsAssetsByContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "js"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "js", "app.js")
	if err := os.WriteFile(file, []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	Use(http.Dir(dir))
	t.Cleanup(func() { source.Store(nil); cache.Clear() })

	first := URL("/assets/js/app.js")
	if !strings.HasPrefix(first, "/assets/js/app.js?v=") || first != URL("/assets/js/app.js") {
		t.Fatalf("URL = %q, want a stable version", first)
	}
	// A new release changes the file, and with it the version.
	if err := os.WriteFile(file, []byte("console.log(2)"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if second := URL("/assets/js/app.js"); second == first {
		t.Fatal("version did not change with the content")
	}
	for _, path := range []string{"/assets/js/missing.js", "/assets/js", "/other/app.js"} {
		if got := URL(path); got != path {
			t.Errorf("URL(%q) = %q, want it unchanged", path, got)
		}
	}
}

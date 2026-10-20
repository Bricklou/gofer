package views

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/buildinfo"
)

func renderAboutTab(t *testing.T, info buildinfo.Info) string {
	t.Helper()
	var out bytes.Buffer
	if err := SettingsAboutTab(info).Render(context.Background(), &out); err != nil {
		t.Fatalf("SettingsAboutTab.Render() error = %v", err)
	}
	return out.String()
}

func TestSettingsAboutTabShowsRelease(t *testing.T) {
	html := renderAboutTab(t, buildinfo.Info{Version: "v0.10.0", Commit: "0123456789abcdef", GoVersion: "go1.27.1"})
	for _, want := range []string{
		"This is a release build of Gofer.",
		`href="https://github.com/cristianadrielbraun/gofer/releases/tag/v0.10.0"`,
		">v0.10.0</a>",
		">0123456</a>",
		"go1.27.1",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("about tab missing %q: %s", want, html)
		}
	}
}

func TestSettingsAboutTabShowsSourceBuild(t *testing.T) {
	html := renderAboutTab(t, buildinfo.Info{
		Commit:     "7e36941abcdef0123456789",
		CommitTime: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		Modified:   true,
	})
	for _, want := range []string{
		"built from source",
		"Development build",
		`href="https://github.com/cristianadrielbraun/gofer/commit/7e36941abcdef0123456789"`,
		"with uncommitted changes",
		"October 19, 2026",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("about tab missing %q: %s", want, html)
		}
	}
	if strings.Contains(html, "/releases/tag/") {
		t.Fatalf("source build should not link a release: %s", html)
	}
}

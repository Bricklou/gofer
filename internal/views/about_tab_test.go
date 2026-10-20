package views

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/buildinfo"
	"github.com/cristianadrielbraun/gofer/internal/updatecheck"
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

func renderUpdateCheck(t *testing.T, result *updatecheck.Result, errMsg string) string {
	t.Helper()
	var out bytes.Buffer
	if err := AboutUpdateCheck(result, errMsg).Render(context.Background(), &out); err != nil {
		t.Fatalf("AboutUpdateCheck.Render() error = %v", err)
	}
	return out.String()
}

func TestAboutUpdateCheckStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *updatecheck.Result
		errMsg string
		want   []string
	}{
		{"unchecked", nil, "", []string{"Not checked yet.", "Check for updates", `hx-post="/api/settings/about/update-check"`}},
		{"failed", nil, "Could not check for updates. Try again later.", []string{"Could not check for updates.", "Check again"}},
		{"release available", &updatecheck.Result{Available: true, Latest: updatecheck.Release{Tag: "v0.11.0", URL: "https://example.test/v0.11.0", Notes: "Faster sync."}}, "", []string{">v0.11.0</a> is available.", "Faster sync."}},
		{"up to date", &updatecheck.Result{Latest: updatecheck.Release{Tag: "v0.10.0"}}, "", []string{"Gofer is up to date."}},
		{"commits behind", &updatecheck.Result{Source: true, Available: true, Behind: 12, Commits: []updatecheck.Commit{{SHA: "2222333344445555", Title: "Newer change", URL: "https://example.test/2222"}}}, "", []string{"12 newer commits on master.", ">2222333</a>", "Newer change"}},
		{"unknown commit", &updatecheck.Result{Source: true, Unknown: true}, "", []string{"GitHub does not have this build's commit"}},
	} {
		html := renderUpdateCheck(t, tc.result, tc.errMsg)
		for _, want := range tc.want {
			if !strings.Contains(html, want) {
				t.Errorf("%s: missing %q: %s", tc.name, want, html)
			}
		}
	}
}

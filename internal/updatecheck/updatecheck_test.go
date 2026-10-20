package updatecheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/buildinfo"
)

func testChecker(t *testing.T, handler http.HandlerFunc) (*Checker, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	return &Checker{client: srv.Client(), apiURL: srv.URL, now: func() time.Time { return now }}, &calls
}

const releasesJSON = `[
	{"tag_name":"v0.11.0-rc.1","prerelease":true,"html_url":"https://example.test/rc"},
	{"tag_name":"v0.10.0","name":"Gofer 0.10","html_url":"https://example.test/v0.10.0","body":"Faster sync.\n"},
	{"tag_name":"v0.9.1","html_url":"https://example.test/v0.9.1"}
]`

func TestReleaseBuildFindsNewerRelease(t *testing.T) {
	c, calls := testChecker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/cristianadrielbraun/gofer/releases" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(releasesJSON))
	})
	r, err := c.Check(context.Background(), buildinfo.Info{Version: "v0.9.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Available || r.Latest.Tag != "v0.10.0" || r.Latest.Notes != "Faster sync." {
		t.Fatalf("got %+v, want v0.10.0 available, skipping the pre-release", r)
	}
	if _, err := c.Check(context.Background(), buildinfo.Info{Version: "v0.9.1"}); err != nil || *calls != 1 {
		t.Fatalf("second check should use the cached answer: calls=%d err=%v", *calls, err)
	}
}

func TestReleaseBuildUpToDateAndPrerelease(t *testing.T) {
	c, _ := testChecker(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(releasesJSON)) })
	if r, _ := c.Check(context.Background(), buildinfo.Info{Version: "v0.10.0"}); r.Available {
		t.Fatalf("v0.10.0 should be up to date: %+v", r)
	}
	if r, _ := c.Check(context.Background(), buildinfo.Info{Version: "v0.10.0-beta.2"}); !r.Available || r.Latest.Tag != "v0.11.0-rc.1" {
		t.Fatalf("a pre-release build should be offered the newer pre-release: %+v", r)
	}
}

func TestSourceBuildListsNewerCommits(t *testing.T) {
	c, _ := testChecker(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/cristianadrielbraun/gofer/compare/abc123...master" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"status":"ahead","ahead_by":2,"html_url":"https://example.test/compare","commits":[
			{"sha":"1111","html_url":"https://example.test/1111","commit":{"message":"Older change\n\nBody","committer":{"date":"2026-10-18T10:00:00Z"}}},
			{"sha":"2222","html_url":"https://example.test/2222","commit":{"message":"Newer change","committer":{"date":"2026-10-19T10:00:00Z"}}}
		]}`))
	})
	r, err := c.Check(context.Background(), buildinfo.Info{Commit: "abc123"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Source || !r.Available || r.Behind != 2 || len(r.Commits) != 2 || r.Commits[0].Title != "Newer change" || r.Commits[1].Title != "Older change" {
		t.Fatalf("got %+v, want 2 commits behind, newest first", r)
	}
}

func TestSourceBuildWithUnpushedCommit(t *testing.T) {
	c, _ := testChecker(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	r, err := c.Check(context.Background(), buildinfo.Info{Commit: "local"})
	if err != nil || !r.Unknown || r.Available {
		t.Fatalf("got %+v, %v; want an unknown commit", r, err)
	}
}

func TestRateLimited(t *testing.T) {
	c, _ := testChecker(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	if _, err := c.Check(context.Background(), buildinfo.Info{Version: "v0.9.1"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v0.10.0", "v0.9.1", 1},
		{"v0.9.1", "v0.9.1-rc.1", 1},
		{"v0.9.1-rc.2", "v0.9.1-rc.10", -1},
		{"v1.0.0-beta.1", "v1.0.0-alpha.3", 1},
		{"v1.2.3", "1.2.3", 0},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestFromSourceBuild(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.27.1",
		Main:      debug.Module{Version: "v0.9.2-0.20261019120000-7e36941abcde+dirty"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "7e36941abcdef0123456789abcdef0123456789a"},
			{Key: "vcs.time", Value: "2026-10-19T12:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	i := from(bi, "", "", "")
	if i.Release() {
		t.Fatalf("source build reported as release %q", i.Version)
	}
	if got, want := i.String(), "dev · 7e36941 (modified)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if i.CommitTime.IsZero() || i.GoVersion != "go1.27.1" {
		t.Fatalf("missing commit time or Go version: %+v", i)
	}
}

func TestFromRelease(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	if got := from(bi, "v0.10.0", "", "").String(); got != "v0.10.0" {
		t.Fatalf("String() = %q, want v0.10.0", got)
	}
	if got := from(&debug.BuildInfo{Main: debug.Module{Version: "v0.9.1"}}, "", "", "").String(); got != "v0.9.1" {
		t.Fatalf("installed tag: String() = %q, want v0.9.1", got)
	}
}

func TestCommitFromFlagWhenNoCheckout(t *testing.T) {
	i := from(&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "", "abc1234def", "")
	if got, want := i.String(), "dev · abc1234"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got := from(nil, "", "", "").String(); got != "dev" {
		t.Fatalf("no build info: String() = %q, want dev", got)
	}
}

func TestCommitFlagIgnoresOtherCheckout(t *testing.T) {
	bi := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "ffff000"},
		{Key: "vcs.time", Value: "2026-10-19T12:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	}}
	i := from(bi, "", "abc1234", "")
	if i.Commit != "abc1234" || i.Modified || !i.CommitTime.IsZero() {
		t.Fatalf("got %+v, want only the passed commit", i)
	}
}

func TestBranch(t *testing.T) {
	if got := from(nil, "", "", "about-settings").Branch; got != "about-settings" {
		t.Fatalf("Branch = %q, want about-settings", got)
	}
	if got := from(nil, "", "", "HEAD").Branch; got != "" {
		t.Fatalf("detached checkout: Branch = %q, want none", got)
	}
}

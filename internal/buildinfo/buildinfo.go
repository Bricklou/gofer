// Package buildinfo reports which version of Gofer is running: the release
// it was built as, or the commit it was built from.
package buildinfo

import (
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Version and Commit are set at build time with
// -ldflags "-X github.com/cristianadrielbraun/gofer/internal/buildinfo.Version=v1.2.3".
// Commit is only needed where the build has no git checkout, such as a
// container build; otherwise Go records the commit itself.
var (
	Version string
	Commit  string
)

// Info describes the running build.
type Info struct {
	// Version is the release version, empty for a build from source.
	Version    string
	Commit     string
	CommitTime time.Time
	// Modified reports a build from a checkout with uncommitted changes.
	Modified  bool
	GoVersion string
}

// Release reports whether this is a build of a tagged release.
func (i Info) Release() bool { return i.Version != "" }

// ShortCommit returns the first seven characters of the commit.
func (i Info) ShortCommit() string {
	if len(i.Commit) > 7 {
		return i.Commit[:7]
	}
	return i.Commit
}

// String returns "v1.2.3" for a release, or "dev · 7e36941 (modified)" for a
// build from source.
func (i Info) String() string {
	if i.Release() {
		return i.Version
	}
	s := "dev"
	if c := i.ShortCommit(); c != "" {
		s += " · " + c
	}
	if i.Modified {
		s += " (modified)"
	}
	return s
}

var (
	once sync.Once
	info Info
)

// Get returns the running build's information.
func Get() Info {
	once.Do(func() {
		bi, _ := debug.ReadBuildInfo()
		info = from(bi, Version, Commit)
	})
	return info
}

// pseudoVersion matches the versions Go makes up for untagged commits, such
// as v0.9.2-0.20261019120000-7e36941abcde.
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)

func from(bi *debug.BuildInfo, version, commit string) Info {
	i := Info{Version: strings.TrimSpace(version), Commit: strings.TrimSpace(commit)}
	if bi == nil {
		return i
	}
	i.GoVersion = bi.GoVersion
	// A module version is a release only when it names a tag exactly, as
	// with "go install ...@v1.2.3"; a local build gets "(devel)", or a
	// made-up version with "+dirty" when the checkout has changes.
	if mv := bi.Main.Version; i.Version == "" && strings.HasPrefix(mv, "v") &&
		!strings.Contains(mv, "+") && !pseudoVersion.MatchString(mv) {
		i.Version = mv
	}
	var vcs Info
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			vcs.Commit = s.Value
		case "vcs.time":
			vcs.CommitTime, _ = time.Parse(time.RFC3339, s.Value)
		case "vcs.modified":
			vcs.Modified = s.Value == "true"
		}
	}
	// The checkout's date and changes describe its own commit, so they do
	// not apply when a different commit was passed in.
	if i.Commit == "" || i.Commit == vcs.Commit {
		i.Commit, i.CommitTime, i.Modified = vcs.Commit, vcs.CommitTime, vcs.Modified
	}
	return i
}

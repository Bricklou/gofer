// Package updatecheck asks GitHub whether a newer Gofer exists than the one
// running: a newer release for a release build, or newer commits on master
// for a build from source. It only reads; nothing is downloaded or changed.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cristianadrielbraun/gofer/internal/buildinfo"
	"github.com/cristianadrielbraun/gofer/internal/netguard"
)

const (
	repository = "cristianadrielbraun/gofer"
	branch     = "master"
	// cacheFor keeps repeated checks from using up GitHub's limit of 60
	// unauthenticated requests an hour.
	cacheFor = 10 * time.Minute
	// maxCommits is how many of the newer commits a source build lists.
	maxCommits = 10
)

// Release is a published Gofer release.
type Release struct {
	Tag         string
	Name        string
	URL         string
	Notes       string
	PublishedAt time.Time
}

// Commit is a commit on master that the running build does not have.
type Commit struct {
	SHA   string
	Title string
	URL   string
	Date  time.Time
}

// Result is the outcome of one check.
type Result struct {
	// Source reports a build from source, compared by commit.
	Source bool
	// Available reports that something newer than the running build exists.
	Available bool

	// Latest is the newest release, for a release build.
	Latest Release

	// Behind counts the commits on master the running build does not have,
	// newest first in Commits (at most maxCommits of them).
	Behind     int
	Commits    []Commit
	CompareURL string
	// Unknown reports a commit GitHub does not have, such as one never
	// pushed, so nothing could be compared.
	Unknown bool
	// Diverged reports that the running build also has commits master
	// does not.
	Diverged bool

	CheckedAt time.Time
}

// ErrRateLimited reports that GitHub refused the check for now.
var ErrRateLimited = errors.New("GitHub is limiting requests right now; try again later")

// Checker asks GitHub and keeps the answer for a while.
type Checker struct {
	client *http.Client
	apiURL string
	now    func() time.Time

	mu     sync.Mutex
	key    string
	cached Result
}

// New returns a Checker that talks to GitHub.
func New() *Checker {
	return &Checker{
		client: &http.Client{Timeout: 10 * time.Second, Transport: netguard.NewHTTPTransport(nil)},
		apiURL: "https://api.github.com",
		now:    time.Now,
	}
}

// Default is the Checker the app uses.
var Default = New()

// Check compares info with what GitHub has, reusing a recent answer.
func (c *Checker) Check(ctx context.Context, info buildinfo.Info) (Result, error) {
	key := info.Version + "@" + info.Commit
	c.mu.Lock()
	if c.key == key && c.now().Sub(c.cached.CheckedAt) < cacheFor {
		r := c.cached
		c.mu.Unlock()
		return r, nil
	}
	c.mu.Unlock()

	var (
		r   Result
		err error
	)
	if info.Release() {
		r, err = c.checkRelease(ctx, info.Version)
	} else {
		r, err = c.checkSource(ctx, info.Commit)
	}
	if err != nil {
		return Result{}, err
	}
	r.CheckedAt = c.now()
	c.mu.Lock()
	c.key, c.cached = key, r
	c.mu.Unlock()
	return r, nil
}

type githubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

func (c *Checker) checkRelease(ctx context.Context, current string) (Result, error) {
	var releases []githubRelease
	if _, err := c.get(ctx, "/repos/"+repository+"/releases?per_page=30", &releases); err != nil {
		return Result{}, err
	}
	// Someone on a pre-release is offered newer pre-releases too; everyone
	// else only final releases.
	wantPre := isPrerelease(current)
	for _, rel := range releases {
		if rel.Draft || (rel.Prerelease && !wantPre) {
			continue
		}
		latest := Release{
			Tag:         rel.TagName,
			Name:        strings.TrimSpace(rel.Name),
			URL:         rel.HTMLURL,
			Notes:       strings.TrimSpace(rel.Body),
			PublishedAt: rel.PublishedAt,
		}
		return Result{Latest: latest, Available: compareVersions(rel.TagName, current) > 0}, nil
	}
	return Result{}, nil
}

type githubCompare struct {
	Status   string `json:"status"`
	AheadBy  int    `json:"ahead_by"`
	BehindBy int    `json:"behind_by"`
	HTMLURL  string `json:"html_url"`
	Commits  []struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message   string `json:"message"`
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	} `json:"commits"`
}

func (c *Checker) checkSource(ctx context.Context, commit string) (Result, error) {
	r := Result{Source: true}
	if commit == "" {
		r.Unknown = true
		return r, nil
	}
	// The compare runs from the running commit to master, so master's
	// commits that the build lacks are the ones "ahead".
	var cmp githubCompare
	status, err := c.get(ctx, "/repos/"+repository+"/compare/"+commit+"..."+branch, &cmp)
	if status == http.StatusNotFound {
		r.Unknown = true
		return r, nil
	}
	if err != nil {
		return Result{}, err
	}
	r.Behind = cmp.AheadBy
	r.Available = cmp.AheadBy > 0
	r.Diverged = cmp.Status == "diverged"
	r.CompareURL = cmp.HTMLURL
	for i := len(cmp.Commits) - 1; i >= 0 && len(r.Commits) < maxCommits; i-- {
		gc := cmp.Commits[i]
		title, _, _ := strings.Cut(gc.Commit.Message, "\n")
		r.Commits = append(r.Commits, Commit{SHA: gc.SHA, Title: strings.TrimSpace(title), URL: gc.HTMLURL, Date: gc.Commit.Committer.Date})
	}
	return r, nil
}

func (c *Checker) get(ctx context.Context, path string, into any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Gofer update check")
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return resp.StatusCode, ErrRateLimited
	case resp.StatusCode != http.StatusOK:
		return resp.StatusCode, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return resp.StatusCode, fmt.Errorf("could not read GitHub's answer: %w", err)
	}
	return resp.StatusCode, nil
}

func isPrerelease(v string) bool {
	_, pre := splitVersion(v)
	return pre != ""
}

// splitVersion turns "v1.2.3-rc.1" into [1 2 3] and "rc.1".
func splitVersion(v string) ([3]int, string) {
	var nums [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "+")
	core, pre, _ := strings.Cut(v, "-")
	for i, part := range strings.SplitN(core, ".", 3) {
		nums[i], _ = strconv.Atoi(part)
	}
	return nums, pre
}

// compareVersions orders two release tags: a final release comes after its
// own pre-releases, and pre-releases compare by their dot-separated parts.
func compareVersions(a, b string) int {
	an, ap := splitVersion(a)
	bn, bp := splitVersion(b)
	for i := range an {
		if an[i] != bn[i] {
			return cmpInt(an[i], bn[i])
		}
	}
	switch {
	case ap == bp:
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	as, bs := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, aerr := strconv.Atoi(as[i])
		bi, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if ai != bi {
				return cmpInt(ai, bi)
			}
		case as[i] != bs[i]:
			return strings.Compare(as[i], bs[i])
		}
	}
	return cmpInt(len(as), len(bs))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

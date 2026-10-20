package handler

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync/atomic"
	"testing"
)

func TestDAVDiscoverySkipsDNSForIPAddresses(t *testing.T) {
	var lookups atomic.Int32
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		lookups.Add(1)
		return nil, errors.New("unexpected DNS lookup for an IP address")
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })

	for _, tc := range []struct{ input, calDAVURL string }{
		{"192.168.0.230", "https://192.168.0.230"},
		{"192.168.0.230:8687", "https://192.168.0.230:8687"},
		{"http://192.168.0.230:8687/dav/", "http://192.168.0.230:8687/dav/"},
		{"https://192.168.0.230/dav/", "https://192.168.0.230/dav/"},
		{"2001:db8::230", "https://[2001:db8::230]"},
		{"[2001:db8::230]:8687", "https://[2001:db8::230]:8687"},
		{"http://[2001:db8::230]:8687/dav/", "http://[2001:db8::230]:8687/dav/"},
		{"https://[::ffff:192.168.0.230]/dav/", "https://[::ffff:192.168.0.230]/dav/"},
		{"http://[fe80::230%25eth0]:8687/dav/", "http://[fe80::230%25eth0]:8687/dav/"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var progress []string
			tracker := &cardDAVDiscoveryTracker{progress: func(_, _ int, endpoint string) { progress = append(progress, endpoint) }}
			if got := expandCardDAVDiscoveryCandidates(t.Context(), []string{tc.input}, tracker); !slices.Equal(got, []string{tc.input}) {
				t.Fatalf("CardDAV candidates = %v, want only %s", got, tc.input)
			}
			if len(progress) != 0 || tracker.total != 0 {
				t.Fatalf("CardDAV scheduled DNS discovery: %v (total %d)", progress, tracker.total)
			}
			if got := calDAVAutodiscoveryCandidates(t.Context(), nil, tc.input); !slices.Equal(got, []string{tc.calDAVURL}) {
				t.Fatalf("CalDAV identity candidates = %v, want only %s", got, tc.calDAVURL)
			}
			if got := calDAVAutodiscoveryCandidates(t.Context(), []string{tc.calDAVURL}, tc.input); !slices.Equal(got, []string{tc.calDAVURL}) {
				t.Fatalf("CalDAV server candidates = %v, want only %s", got, tc.calDAVURL)
			}
		})
	}
	if got := lookups.Load(); got != 0 {
		t.Fatalf("IP discovery attempted %d DNS queries", got)
	}
}

func TestDAVDiscoveryKeepsDomainGuesses(t *testing.T) {
	for _, domains := range []struct {
		name string
		get  func(string) []string
	}{{"CardDAV", cardDAVDiscoveryDomains}, {"CalDAV", calDAVDiscoveryDomains}} {
		t.Run(domains.name, func(t *testing.T) {
			for _, tc := range []struct {
				input string
				want  []string
			}{
				{"mail.example.test", []string{"mail.example.test", "example.test"}},
				{"example.test", []string{"example.test"}},
				{"192.168.0.230", nil},
				{"2001:db8::230", nil},
				{"::ffff:192.168.0.230", nil},
				{"fe80::230%eth0", nil},
			} {
				if got := domains.get(tc.input); !slices.Equal(got, tc.want) {
					t.Errorf("domains(%q) = %v, want %v", tc.input, got, tc.want)
				}
			}
		})
	}
}

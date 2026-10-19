package autodiscover

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const jmapWellKnownPath = "/.well-known/jmap"

// JMAPSessionCandidates lists the session URLs worth trying for an account,
// best first. It only looks up DNS and never connects.
//
// With server empty it follows RFC 8620 section 2.2: the _jmap._tcp SRV
// records of the email's domain, then https://<domain>/.well-known/jmap.
// A server with a scheme or a path is a session URL and is used as given. A
// bare host, optionally with a port, is tried over HTTPS and then over plain
// HTTP; callers connect over plain HTTP only where an administrator approved
// it.
func JMAPSessionCandidates(ctx context.Context, email, server string, resolver SRVResolver) ([]string, error) {
	server = strings.TrimSpace(server)
	if server != "" {
		return jmapServerCandidates(server)
	}
	parts, err := parseEmail(email)
	if err != nil {
		return nil, err
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	var candidates []string
	for _, target := range lookupJMAPService(ctx, resolver, parts.Domain) {
		candidates = append(candidates, "https://"+net.JoinHostPort(target.Host, strconv.Itoa(target.Port))+jmapWellKnownPath)
	}
	candidates = append(candidates, "https://"+parts.Domain+jmapWellKnownPath)
	return dedupeStrings(candidates), nil
}

func jmapServerCandidates(server string) ([]string, error) {
	if strings.Contains(server, "://") {
		parsed, err := url.Parse(server)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return nil, errors.New("enter the server as a host name, or as an https:// or http:// session URL")
		}
		return []string{server}, nil
	}
	if strings.Contains(server, "/") {
		// A host with a path but no scheme: a session URL missing https://.
		return jmapServerCandidates("https://" + server)
	}
	host, port := server, ""
	if h, p, err := net.SplitHostPort(server); err == nil {
		host, port = h, p
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("enter a port between 1 and 65535")
		}
	}
	host = cleanHost(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if host == "" || strings.ContainsAny(host, " @?#") {
		return nil, errors.New("enter a valid server address")
	}
	address := host
	if strings.Contains(host, ":") {
		address = "[" + host + "]"
	}
	if port != "" {
		address = net.JoinHostPort(host, port)
	}
	return []string{
		"https://" + address + jmapWellKnownPath,
		"http://" + address + jmapWellKnownPath,
	}, nil
}

// lookupJMAPService returns the domain's JMAP SRV targets by priority, and by
// weight within a priority. A lone "." target means the domain offers no
// JMAP service (RFC 2782).
func lookupJMAPService(ctx context.Context, resolver SRVResolver, domain string) []endpoint {
	targets := lookupService(ctx, resolver, "jmap", domain, "tls")
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Priority != targets[j].Priority {
			return targets[i].Priority < targets[j].Priority
		}
		return targets[i].Weight > targets[j].Weight
	})
	return targets
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := values[:0]
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

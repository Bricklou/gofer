package jmap

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Endpoint is the scheme, host and port a URL connects to.
type Endpoint struct {
	Scheme string
	Host   string
	Port   int
}

func (e Endpoint) String() string {
	host := e.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s://%s:%d", e.Scheme, host, e.Port)
}

// ParseEndpoint returns where an http or https URL connects, filling in the
// default port. The host is lowercased without brackets or a trailing dot, so
// two spellings of the same server compare equal.
func ParseEndpoint(rawURL string) (Endpoint, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Endpoint{}, err
	}
	scheme := strings.ToLower(parsed.Scheme)
	port := 0
	switch scheme {
	case "https":
		port = 443
	case "http":
		port = 80
	default:
		return Endpoint{}, fmt.Errorf("jmap: unsupported URL scheme %q", parsed.Scheme)
	}
	host := NormalizeHost(parsed.Hostname())
	if host == "" {
		return Endpoint{}, errors.New("jmap: URL has no host")
	}
	if text := parsed.Port(); text != "" {
		if port, err = strconv.Atoi(text); err != nil || port < 1 || port > 65535 {
			return Endpoint{}, fmt.Errorf("jmap: invalid port %q", text)
		}
	}
	return Endpoint{Scheme: scheme, Host: host, Port: port}, nil
}

// NormalizeHost lowercases a host name and drops IPv6 brackets and a trailing
// dot.
func NormalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

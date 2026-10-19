package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/cristianadrielbraun/gofer/internal/netguard"
)

// davHTTPTransport carries every CalDAV and CardDAV request. People enter
// those servers themselves, so connections go through netguard and reach a
// private address only when the mail security exceptions approve it. Tests
// replace the transport to reach their local servers.
var davHTTPTransport http.RoundTripper = netguard.NewHTTPTransport(davPrivateTargetAllowed)

var davPrivateTargetPolicy atomic.Pointer[netguard.PrivatePolicy]

// setDAVPrivateTargetPolicy points DAV connections at the instance's mail
// security exceptions. The transport is shared by every request, so the
// policy is set once, when the handler is built.
func setDAVPrivateTargetPolicy(policy netguard.PrivatePolicy) {
	davPrivateTargetPolicy.Store(&policy)
}

func davPrivateTargetAllowed(ctx context.Context, protocol, host string, port int) bool {
	policy := davPrivateTargetPolicy.Load()
	return policy != nil && *policy != nil && (*policy)(ctx, protocol, host, port)
}

// calDAVHTTPTransport carries every CalDAV request. Requests go out over
// HTTPS, or over plain HTTP only to a host and port an administrator approved
// with an HTTP plaintext exception, as JMAP servers do. The check runs on each
// request, so it covers redirects as well.
var calDAVHTTPTransport http.RoundTripper = calDAVPlaintextGuard{}

var davPlaintextPolicy atomic.Pointer[func(context.Context, string, int) bool]

// setDAVPlaintextPolicy points CalDAV's plain HTTP check at the instance's
// mail security exceptions, once, when the handler is built.
func setDAVPlaintextPolicy(allowed func(ctx context.Context, host string, port int) bool) {
	davPlaintextPolicy.Store(&allowed)
}

func davPlaintextAllowed(ctx context.Context, host string, port int) bool {
	policy := davPlaintextPolicy.Load()
	return policy != nil && *policy != nil && (*policy)(ctx, host, port)
}

// calDAVURLScheme reports whether a CalDAV URL may use scheme at all; whether
// a plain HTTP server is approved is checked when connecting.
func calDAVURLScheme(scheme string) bool {
	return strings.EqualFold(scheme, "https") || strings.EqualFold(scheme, "http")
}

type calDAVPlaintextGuard struct{}

func (calDAVPlaintextGuard) RoundTrip(req *http.Request) (*http.Response, error) {
	switch strings.ToLower(req.URL.Scheme) {
	case "https":
	case "http":
		host := req.URL.Hostname()
		port, _ := strconv.Atoi(davURLPort(req.URL))
		if !davPlaintextAllowed(req.Context(), host, port) {
			return nil, fmt.Errorf("Gofer won't connect to CalDAV server %s without encryption; an administrator can add an HTTP plaintext exception for %s:%d under Mail security", req.URL.Host, host, port)
		}
	default:
		return nil, fmt.Errorf("CalDAV does not support %q URLs", req.URL.Scheme)
	}
	return davHTTPTransport.RoundTrip(req)
}

// validPushEndpoint accepts the HTTPS address of a browser's push service.
// The browser supplies it, so it is delivered to only through netguard as
// well; this refuses anything that isn't a plain HTTPS URL to begin with.
func validPushEndpoint(endpoint string) bool {
	if len(endpoint) > 2048 {
		return false
	}
	u, err := url.Parse(endpoint)
	return err == nil && strings.EqualFold(u.Scheme, "https") && u.Host != "" && u.User == nil
}

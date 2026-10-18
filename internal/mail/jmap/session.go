// Package jmap is a small JMAP (RFC 8620/8621) client covering only what
// Gofer's mail sync needs. It does not import any Gofer package; callers pass
// in the *http.Client so network policy stays with them.
package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	CapabilityCore       = "urn:ietf:params:jmap:core"
	CapabilityMail       = "urn:ietf:params:jmap:mail"
	CapabilitySubmission = "urn:ietf:params:jmap:submission"

	maxSessionBytes = 1 << 20
	maxErrorBytes   = 4096
)

// Auth adds credentials to an outgoing request.
type Auth func(*http.Request)

func BasicAuth(username, password string) Auth {
	return func(req *http.Request) { req.SetBasicAuth(username, password) }
}

func BearerAuth(token string) Auth {
	return func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+token) }
}

type Client struct {
	HTTP *http.Client
	Auth Auth
}

// Session is the JMAP session object (RFC 8620 section 2).
type Session struct {
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
	Accounts        map[string]Account         `json:"accounts"`
	PrimaryAccounts map[string]string          `json:"primaryAccounts"`
	Username        string                     `json:"username"`
	APIURL          string                     `json:"apiUrl"`
	DownloadURL     string                     `json:"downloadUrl"`
	UploadURL       string                     `json:"uploadUrl"`
	EventSourceURL  string                     `json:"eventSourceUrl"`
	State           string                     `json:"state"`

	// Core holds the server limits from the core capability.
	Core CoreCapability `json:"-"`
}

type Account struct {
	Name                string                     `json:"name"`
	IsPersonal          bool                       `json:"isPersonal"`
	IsReadOnly          bool                       `json:"isReadOnly"`
	AccountCapabilities map[string]json.RawMessage `json:"accountCapabilities"`
}

type CoreCapability struct {
	MaxSizeUpload         int64 `json:"maxSizeUpload"`
	MaxConcurrentUpload   int   `json:"maxConcurrentUpload"`
	MaxSizeRequest        int64 `json:"maxSizeRequest"`
	MaxConcurrentRequests int   `json:"maxConcurrentRequests"`
	MaxCallsInRequest     int   `json:"maxCallsInRequest"`
	MaxObjectsInGet       int   `json:"maxObjectsInGet"`
	MaxObjectsInSet       int   `json:"maxObjectsInSet"`
}

// HTTPError is a non-2xx response from the server.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("jmap: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("jmap: HTTP %d: %s", e.StatusCode, e.Body)
}

// PrimaryAccount returns the primary account id for a capability, or "".
func (s *Session) PrimaryAccount(capability string) string {
	return s.PrimaryAccounts[capability]
}

// Session fetches the session object. sessionURL is usually
// https://host/.well-known/jmap; redirects are followed.
func (c *Client) Session(ctx context.Context, sessionURL string) (*Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.Auth != nil {
		c.Auth(req)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	var session Session
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSessionBytes)).Decode(&session); err != nil {
		return nil, fmt.Errorf("jmap: decode session: %w", err)
	}
	if raw, ok := session.Capabilities[CapabilityCore]; ok {
		if err := json.Unmarshal(raw, &session.Core); err != nil {
			return nil, fmt.Errorf("jmap: decode core capability: %w", err)
		}
	}

	// Servers may send these relative; resolve them against the URL the
	// session actually came from (after redirects).
	base := resp.Request.URL
	for _, field := range []*string{&session.APIURL, &session.DownloadURL, &session.UploadURL, &session.EventSourceURL} {
		*field = resolveTemplateURL(base, *field)
	}
	if session.APIURL == "" {
		return nil, fmt.Errorf("jmap: session has no apiUrl")
	}
	return &session, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// resolveTemplateURL makes a possibly relative URL absolute. It works on the
// string because downloadUrl and friends are URI templates, and url.URL would
// escape their {placeholders}.
func resolveTemplateURL(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "://") {
		return ref
	}
	origin := base.Scheme + "://" + base.Host
	if strings.HasPrefix(ref, "/") {
		return origin + ref
	}
	dir := base.Path[:strings.LastIndex(base.Path, "/")+1]
	if dir == "" {
		dir = "/"
	}
	return origin + dir + ref
}

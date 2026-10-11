package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const injectedColor = `rgb(</style><script>alert(document.domain)</script><x)`

func TestSafeEmailCSSColorAcceptsOnlyPlainColors(t *testing.T) {
	for _, value := range []string{"#f8f2e6", "#ABC", "rgb(42, 37, 32)", "rgba(42,37,32,0.5)", "rgba(42, 37, 32, .5)", " #2a2520 "} {
		if got := safeEmailCSSColor(value, "fallback"); got != strings.TrimSpace(value) {
			t.Errorf("safeEmailCSSColor(%q) = %q, want it kept", value, got)
		}
	}
	for _, value := range []string{
		injectedColor,
		"rgb(1,2,3);color:red",
		"rgb(1,2,3)}body{x:y",
		"rgb(1,2,3) </style>",
		`rgb(1,2,3)"`,
		"rgb(var(--x),2,3)",
		"rgb(1,2)",
		"#abcd",
		"#ggg",
		"red",
		"url(https://example.test/x)",
	} {
		if got := safeEmailCSSColor(value, "fallback"); got != "fallback" {
			t.Errorf("safeEmailCSSColor(%q) = %q, want fallback", value, got)
		}
	}
}

// The reader's colors arrive in the body URL, so a crafted link could carry
// markup into the document. They must be dropped, and the document must stay
// sandboxed even when the link is opened outside the reader's iframe.
func TestOwnedEmailBodyIgnoresInjectedColorsAndStaysSandboxed(t *testing.T) {
	f := newOwnedMessageContentFixture(t)
	query := url.Values{"theme": {"light"}, "bg": {injectedColor}, "fg": {injectedColor}, "link": {injectedColor}}
	rec := httptest.NewRecorder()
	f.h.handleUserEmailBody(rec, ownedContentRequest("alice", "/email/1/body?"+query.Encode(), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "alert(document.domain)") || !strings.Contains(body, "alice private body") {
		t.Fatalf("body carries the injected color or lost the message: %q", body)
	}
	assertEmailBodyDocumentSandboxed(t, rec)
}

func TestEmailBodyErrorDocumentStaysSandboxed(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Handler{}).writeEmailBodyError(rec, context.Background(), "owner", "42", "", sql.ErrNoRows)
	assertEmailBodyDocumentSandboxed(t, rec)
}

func assertEmailBodyDocumentSandboxed(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	policy := rec.Header().Get("Content-Security-Policy")
	if !strings.HasPrefix(policy, "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox;") || strings.Contains(policy, "allow-same-origin") {
		t.Fatalf("Content-Security-Policy = %q, want the reader's sandbox without allow-same-origin", policy)
	}
	if !strings.Contains(policy, "frame-ancestors 'self'") {
		t.Fatalf("Content-Security-Policy = %q, lost frame-ancestors", policy)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("headers = %v", rec.Header())
	}
}

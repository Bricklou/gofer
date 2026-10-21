package httpguard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOutlookWebhooksRequireAPublicHTTPSAddress(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		insecureLAN bool
		allowRemote bool
		authEnabled bool
		wantError   string
	}{
		{name: "public host name", baseURL: "https://mail.example.com", authEnabled: true},
		{name: "public address", baseURL: "https://203.0.113.10", authEnabled: true},
		{name: "unauthenticated", baseURL: "https://mail.example.com", allowRemote: true, wantError: "requires personal or managed authentication"},
		{name: "loopback", baseURL: "https://localhost:8090", authEnabled: true, wantError: "public https address"},
		{name: "private address", baseURL: "https://192.168.1.20", authEnabled: true, wantError: "public https address"},
		{name: "plain http on the LAN", baseURL: "http://192.168.1.20:8090", insecureLAN: true, authEnabled: true, wantError: "public https address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := newConfig("127.0.0.1:8090", tt.baseURL, tt.allowRemote)
			if err != nil {
				t.Fatal(err)
			}
			cfg.AllowInsecureLAN = tt.insecureLAN
			cfg.OutlookWebhooks = true
			err = cfg.ValidateExposure(tt.authEnabled)
			if tt.wantError == "" && err != nil {
				t.Fatalf("ValidateExposure() error = %v", err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("ValidateExposure() error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestOutlookWebhookURLFollowsTheSetting(t *testing.T) {
	t.Setenv("GOFER_BASE_URL", "https://mail.example.com/")
	t.Setenv("GOFER_OUTLOOK_WEBHOOKS", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.OutlookWebhookURL(); got != "" {
		t.Fatalf("OutlookWebhookURL() while off = %q", got)
	}
	t.Setenv("GOFER_OUTLOOK_WEBHOOKS", "true")
	if cfg, err = LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.OutlookWebhookURL(), "https://mail.example.com/webhooks/outlook"; got != want {
		t.Fatalf("OutlookWebhookURL() = %q, want %q", got, want)
	}
	t.Setenv("GOFER_OUTLOOK_WEBHOOKS", "sometimes")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("LoadConfig() accepted an invalid GOFER_OUTLOOK_WEBHOOKS")
	}
}

// Graph posts from its own servers without an Origin or a session, and from
// addresses an allowlist may not name.
func TestOutlookWebhookPassesOriginAndNetworkChecksOnlyWhenEnabled(t *testing.T) {
	for _, tt := range []struct {
		enabled, skipAllowlist bool
		peer                   string
		want                   int
	}{
		{enabled: false, peer: "192.0.2.1:443", want: http.StatusForbidden},
		{enabled: true, peer: "192.0.2.1:443", want: http.StatusAccepted},
		{enabled: true, peer: "203.0.113.1:443", want: http.StatusForbidden},
		{enabled: true, skipAllowlist: true, peer: "203.0.113.1:443", want: http.StatusAccepted},
	} {
		cfg, err := newConfig("127.0.0.1:8090", "https://mail.example.com", false)
		if err != nil {
			t.Fatal(err)
		}
		cfg.OutlookWebhooks = tt.enabled
		cfg.OutlookWebhooksSkipAllowedCIDRs = tt.skipAllowlist
		cfg.allowedCIDRs, _ = parseCIDRs("test", "192.0.2.0/24")
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) })
		handler := cfg.ClientNetworkMiddleware(true, cfg.Middleware(next))
		serve := func(path, host string) int {
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
			r.Host = host
			r.RemoteAddr = tt.peer
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w.Code
		}
		if got := serve(OutlookWebhookPath, "mail.example.com"); got != tt.want {
			t.Fatalf("%+v: webhook status = %d, want %d", tt, got, tt.want)
		}
		// Other paths keep every check.
		if got := serve("/compose", "mail.example.com"); got != http.StatusForbidden {
			t.Fatalf("%+v: /compose status = %d", tt, got)
		}
		// The host must still be Gofer's own.
		if got := serve(OutlookWebhookPath, "attacker.example"); got != http.StatusMisdirectedRequest && got != http.StatusForbidden {
			t.Fatalf("%+v: foreign host status = %d", tt, got)
		}
	}
}

func TestSkippingTheAllowlistForOutlookIsConfirmedSeparately(t *testing.T) {
	cfg, err := newConfig("127.0.0.1:8090", "https://mail.example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OutlookWebhooksSkipAllowedCIDRs = true
	if err := cfg.ValidateExposure(true); err == nil || !strings.Contains(err.Error(), "requires GOFER_OUTLOOK_WEBHOOKS=true") {
		t.Fatalf("skip without webhooks: ValidateExposure() error = %v", err)
	}

	cfg.OutlookWebhooks = true
	cfg.OutlookWebhooksSkipAllowedCIDRs = false
	if cfg.WarnOutlookWebhooksBehindAllowlist() {
		t.Fatal("warned without an allowlist")
	}
	cfg.allowedCIDRs, _ = parseCIDRs("test", "192.0.2.0/24")
	if !cfg.WarnOutlookWebhooksBehindAllowlist() {
		t.Fatal("no warning with an allowlist and no confirmation")
	}
	cfg.OutlookWebhooksSkipAllowedCIDRs = true
	if cfg.WarnOutlookWebhooksBehindAllowlist() {
		t.Fatal("warned after skipping the allowlist was confirmed")
	}
}

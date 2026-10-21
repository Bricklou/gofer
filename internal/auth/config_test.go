package auth

import (
	"os"
	"slices"
	"testing"
)

func TestLoadConfigSeparatesGoogleLoginFromMailboxOAuth(t *testing.T) {
	t.Setenv("GOFER_AUTH_ENABLED", "true")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "mailbox-client")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "mailbox-secret")
	t.Setenv("GOFER_GOOGLE_LOGIN_CLIENT_ID", "login-client")
	t.Setenv("GOFER_GOOGLE_LOGIN_CLIENT_SECRET", "login-secret")

	cfg := LoadConfig("https://gofer.example")
	if !cfg.Enabled || cfg.GoogleLoginClient == nil {
		t.Fatalf("authentication config = %#v", cfg)
	}
	if cfg.GoogleLoginClient.ClientID != "login-client" || cfg.GoogleLoginClient.ClientSecret != "login-secret" {
		t.Fatalf("Google login client = %#v", cfg.GoogleLoginClient)
	}
	if cfg.GoogleLoginClient.RedirectURL != "https://gofer.example/auth/google/login/callback" {
		t.Fatalf("Google login redirect = %q", cfg.GoogleLoginClient.RedirectURL)
	}
	wantScopes := []string{"openid", "email", "profile"}
	if !slices.Equal(cfg.GoogleLoginClient.Scopes, wantScopes) {
		t.Fatalf("Google login scopes = %#v, want %#v", cfg.GoogleLoginClient.Scopes, wantScopes)
	}
	for _, scope := range cfg.GoogleLoginClient.Scopes {
		if scope == "https://mail.google.com/" || scope == "https://www.googleapis.com/auth/contacts" {
			t.Fatalf("Google application login requested mailbox scope %q", scope)
		}
	}
}

func TestLoadConfigKeepsLocalAuthenticationEnabledWithoutGoogleLogin(t *testing.T) {
	t.Setenv("GOFER_AUTH_ENABLED", "true")
	t.Setenv("GOFER_GOOGLE_LOGIN_CLIENT_ID", "")
	t.Setenv("GOFER_GOOGLE_LOGIN_CLIENT_SECRET", "")

	cfg := LoadConfig("https://gofer.example")
	if !cfg.Enabled {
		t.Fatal("local authentication was disabled because Google login was not configured")
	}
	if cfg.GoogleLoginClient != nil {
		t.Fatalf("Google login client = %#v, want nil", cfg.GoogleLoginClient)
	}
}

func TestLoadConfigDefaultsToPersonalMode(t *testing.T) {
	for _, test := range []struct {
		name, mode, legacy string
		unsetLegacy        bool
		want               Mode
	}{
		{name: "nothing set", unsetLegacy: true, want: ModePersonal},
		{name: "empty mode", mode: "", unsetLegacy: true, want: ModePersonal},
		{name: "explicit open", mode: "open", unsetLegacy: true, want: ModeOpen},
		{name: "legacy disabled", legacy: "false", want: ModeOpen},
		{name: "legacy enabled", legacy: "true", want: ModeManaged},
		{name: "mode wins over legacy", mode: "personal", legacy: "false", want: ModePersonal},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GOFER_AUTH_MODE", test.mode)
			t.Setenv("GOFER_AUTH_ENABLED", test.legacy)
			if test.unsetLegacy {
				os.Unsetenv("GOFER_AUTH_ENABLED")
			}
			cfg := LoadConfig("")
			if got := cfg.AuthenticationMode(); got != test.want || cfg.Enabled != (test.want != ModeOpen) {
				t.Fatalf("mode = %q enabled = %t, want %q", got, cfg.Enabled, test.want)
			}
		})
	}
}

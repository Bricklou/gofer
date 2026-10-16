package providers

import (
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

func TestApplyICloudReplacesServerSettings(t *testing.T) {
	req := models.CreateAccountRequest{
		Provider:     "gmail",
		EmailAddress: "robin@example.org",
		IMAPHost:     "imap.example.org",
		IMAPPort:     143,
		IMAPTLSMode:  "plaintext",
		SMTPHost:     "smtp.example.org",
		SMTPPort:     25,
		SMTPTLSMode:  "plaintext",
		Username:     " robin.lane@icloud.com ",
		Password:     "abcd-efgh-ijkl-mnop",
		AuthMethod:   "oauth2",
		SmtpUsername: "someone-else",
		SmtpPassword: "other",
	}
	ApplyICloud(&req)
	want := models.CreateAccountRequest{
		Provider:     ProviderIMAP,
		EmailAddress: "robin@example.org",
		IMAPHost:     ICloudIMAPHost,
		IMAPPort:     993,
		IMAPTLSMode:  "tls",
		SMTPHost:     ICloudSMTPHost,
		SMTPPort:     587,
		SMTPTLSMode:  "starttls",
		Username:     "robin.lane@icloud.com",
		Password:     "abcd-efgh-ijkl-mnop",
		AuthMethod:   "plain",
	}
	if req != want {
		t.Fatalf("ApplyICloud() = %#v, want %#v", req, want)
	}
}

func TestApplyICloudDefaultsLoginToEmailAddress(t *testing.T) {
	req := models.CreateAccountRequest{EmailAddress: "robin@icloud.com"}
	ApplyICloud(&req)
	if req.Username != "robin@icloud.com" {
		t.Fatalf("Username = %q, want the email address", req.Username)
	}
}

func TestIsICloudAccount(t *testing.T) {
	for _, tc := range []struct {
		provider, auth, host string
		want                 bool
	}{
		{"imap", "plain", "imap.mail.me.com", true},
		{"imap", "plain", " IMAP.MAIL.ME.COM. ", true},
		{"imap", "plain", "imap.example.org", false},
		{"imap", "oauth2", "imap.mail.me.com", false},
		{"gmail", "plain", "imap.mail.me.com", false},
	} {
		if got := IsICloudAccount(tc.provider, tc.auth, tc.host); got != tc.want {
			t.Errorf("IsICloudAccount(%q, %q, %q) = %v, want %v", tc.provider, tc.auth, tc.host, got, tc.want)
		}
	}
}

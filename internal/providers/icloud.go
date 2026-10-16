package providers

import (
	"strings"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

// iCloud Mail is an IMAP account: Apple offers no API or OAuth sign-in for
// other mail apps, only an app-specific password. Gofer stores it with the
// IMAP provider and recognizes it by Apple's servers, so mail, contacts and
// calendar all run through the IMAP, CardDAV and CalDAV code.
const (
	MailServiceICloud = "icloud"

	ICloudIMAPHost    = "imap.mail.me.com"
	ICloudIMAPPort    = 993
	ICloudIMAPTLSMode = "tls"
	ICloudSMTPHost    = "smtp.mail.me.com"
	ICloudSMTPPort    = 587
	ICloudSMTPTLSMode = "starttls"
	ICloudCardDAVURL  = "https://contacts.icloud.com"
	ICloudCalDAVURL   = "https://caldav.icloud.com"

	ICloudAppPasswordURL = "https://account.apple.com"
)

// IsICloudMailHost reports whether host is one of iCloud Mail's servers.
func IsICloudMailHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == ICloudIMAPHost || host == ICloudSMTPHost
}

// IsICloudAccount reports whether a saved account is an iCloud Mail account.
func IsICloudAccount(provider, authMethod, imapHost string) bool {
	return provider == ProviderIMAP && authMethod == "plain" && IsICloudMailHost(imapHost)
}

// IsICloudAddressDomain reports whether email is an address Apple hosts
// itself, as opposed to a custom domain on iCloud+.
func IsICloudAddressDomain(email string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(email[at+1:])) {
	case "icloud.com", "me.com", "mac.com":
		return true
	}
	return false
}

// ApplyICloud fills in iCloud's servers. The forms for iCloud accounts don't
// show server settings, so whatever they send for them is replaced. One
// login, the Apple Account's email address, is used for IMAP, SMTP, CardDAV
// and CalDAV; it defaults to the account's address.
func ApplyICloud(req *models.CreateAccountRequest) {
	req.Provider = ProviderIMAP
	req.AuthMethod = "plain"
	req.IMAPHost, req.IMAPPort, req.IMAPTLSMode = ICloudIMAPHost, ICloudIMAPPort, ICloudIMAPTLSMode
	req.SMTPHost, req.SMTPPort, req.SMTPTLSMode = ICloudSMTPHost, ICloudSMTPPort, ICloudSMTPTLSMode
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		req.Username = strings.TrimSpace(req.EmailAddress)
	}
	req.SmtpUsername, req.SmtpPassword = "", ""
}

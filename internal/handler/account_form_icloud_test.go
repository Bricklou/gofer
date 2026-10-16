package handler

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAccountRequestFromFormFillsICloudServers(t *testing.T) {
	form := url.Values{
		"provider":      {"imap"},
		"mail_service":  {"icloud"},
		"email_address": {"robin@example.org"},
		"imap_host":     {"imap.example.org"},
		"smtp_host":     {"smtp.example.org"},
		"username":      {""},
		"password":      {"abcd-efgh-ijkl-mnop"},
	}
	r := httptest.NewRequest("POST", "/api/accounts", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req := accountRequestFromForm(r)
	if req.IMAPHost != "imap.mail.me.com" || req.IMAPPort != 993 || req.SMTPHost != "smtp.mail.me.com" || req.SMTPPort != 587 || req.SMTPTLSMode != "starttls" {
		t.Fatalf("iCloud servers = %#v", req)
	}
	if req.Username != "robin@example.org" || req.AuthMethod != "plain" || req.Provider != "imap" {
		t.Fatalf("iCloud login = %#v", req)
	}
}

func TestAccountRequestFromFormKeepsTypedServersWithoutICloud(t *testing.T) {
	form := url.Values{"imap_host": {"imap.example.org"}, "smtp_host": {"smtp.example.org"}}
	r := httptest.NewRequest("POST", "/api/accounts", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if req := accountRequestFromForm(r); req.IMAPHost != "imap.example.org" || req.SMTPHost != "smtp.example.org" {
		t.Fatalf("typed servers = %#v", req)
	}
}

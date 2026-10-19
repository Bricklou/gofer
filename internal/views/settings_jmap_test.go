package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

func TestEditAccountDialogShowsJMAPServerAndLogin(t *testing.T) {
	var out bytes.Buffer
	data := models.EditAccountData{
		AccountID: "acc-jmap", Provider: "jmap", AuthMethod: "plain",
		EmailAddress: "robin@example.test", DisplayName: "Robin", Username: "robin@example.test",
		JMAPSessionURL: "https://mail.example.test/.well-known/jmap", EmailSyncEnabled: true,
	}
	if err := EditAccountDialog(data).Render(context.Background(), &out); err != nil {
		t.Fatalf("EditAccountDialog.Render() error = %v", err)
	}
	html := out.String()
	for _, want := range []string{
		`name="provider" value="jmap"`,
		`name="jmap_session_url"`,
		`value="https://mail.example.test/.well-known/jmap"`,
		`name="username"`,
		`name="password"`,
		"Server and login",
		"data-wizard-skip-sending",
		`/api/accounts/acc-jmap/test-changes`,
		"Gofer finds your JMAP server and signs in",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("JMAP edit dialog missing %q", want)
		}
	}
	for _, notWant := range []string{
		`name="imap_host"`,
		`name="smtp_host"`,
		`name="smtp_username"`,
		`name="auth_method"`,
		`data-account-wizard-step="2"`,
	} {
		if strings.Contains(html, notWant) {
			t.Fatalf("JMAP edit dialog should not render %q", notWant)
		}
	}
}

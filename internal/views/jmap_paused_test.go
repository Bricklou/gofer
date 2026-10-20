package views

import (
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/models"
)

// While JMAP is turned off, a JMAP account shows a warning, apart from the
// sync failure one, in the sidebar and in the settings, saying how to turn
// JMAP on. Other accounts, and JMAP accounts while JMAP is on, don't.
func TestJMAPAccountShowsPausedWhileJMAPIsOff(t *testing.T) {
	jmapAccount := models.Account{ID: "acc-jmap", Provider: "jmap", Name: "JMAP", Email: "robin@example.test", EmailSyncEnabled: true}
	imapAccount := models.Account{ID: "acc-imap", Provider: "imap", Name: "IMAP", Email: "robin@example.org", EmailSyncEnabled: true}
	render := func(account models.Account) (sidebar, settings string) {
		t.Helper()
		var out strings.Builder
		if err := SidebarAccountSection(account, "", nil, models.EmailFilters{}).Render(t.Context(), &out); err != nil {
			t.Fatal(err)
		}
		sidebar = out.String()
		out.Reset()
		if err := SettingsAccountCard(account).Render(t.Context(), &out); err != nil {
			t.Fatal(err)
		}
		return sidebar, out.String()
	}
	for _, tc := range []struct {
		name    string
		on      string
		account models.Account
		paused  bool
	}{
		{"JMAP account, JMAP off", "false", jmapAccount, true},
		{"JMAP account, JMAP on", "true", jmapAccount, false},
		{"IMAP account, JMAP off", "false", imapAccount, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOFER_JMAP_EXPERIMENT", tc.on)
			sidebar, settings := render(tc.account)
			for where, html := range map[string]string{"sidebar": sidebar, "settings": settings} {
				shown := strings.Contains(html, `data-account-jmap-paused="acc-jmap"`) || strings.Contains(html, `data-settings-account-jmap-paused="acc-jmap"`)
				if shown != tc.paused {
					t.Fatalf("%s shows the paused warning: %v, want %v", where, shown, tc.paused)
				}
				if tc.paused && !strings.Contains(html, "GOFER_JMAP_EXPERIMENT=true") {
					t.Fatalf("%s paused warning doesn't name the setting:\n%s", where, html)
				}
			}
			// The settings card always carries a hidden sync failure popover;
			// the sidebar shows one only for a failure.
			if strings.Contains(sidebar, "Mail sync failed") {
				t.Fatal("the sidebar shows a paused account as a sync failure")
			}
		})
	}
}

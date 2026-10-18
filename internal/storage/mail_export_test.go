package storage

import (
	"path/filepath"
	"testing"
)

// Folders are named by their place under their parents, whether the server
// names a folder by its full path or by its own name only.
func TestMailExportFolderPaths(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Write().ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO users (id, username, username_normalized, name) VALUES ('owner', 'owner', 'owner', 'Owner')`)
	exec(`INSERT INTO accounts (id, user_id, provider, email_address, display_name, imap_host, imap_port, imap_tls_mode, smtp_host, smtp_port, smtp_tls_mode, username)
		VALUES ('acc', 'owner', 'imap', 'robin@example.test', 'Robin', 'imap.example.test', 993, 'tls', 'smtp.example.test', 465, 'tls', 'robin')`)
	for _, folder := range []struct {
		id, parent, name, role string
		selectable, order      int
	}{
		{"inbox", "", "Inbox", "inbox", 1, 0},
		{"projects", "", "Projects", "custom", 0, 1},
		{"garden", "projects", "Projects/Garden", "custom", 1, 2},
		{"beds", "garden", "Projects/Garden/Raised beds", "custom", 1, 3},
		{"work", "", "Work", "custom", 1, 4},
		{"clients", "work", "Clients", "custom", 1, 5},
		{"starred", "", "Starred", "starred", 1, 6},
	} {
		var parent any
		if folder.parent != "" {
			parent = folder.parent
		}
		exec(`INSERT INTO folders (id, account_id, parent_id, remote_id, name, role, selectable, sort_order) VALUES (?, 'acc', ?, ?, ?, ?, ?, ?)`,
			folder.id, parent, folder.name, folder.name, folder.role, folder.selectable, folder.order)
	}
	folders, err := db.MailExportFolders(ctx, "acc")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, folder := range folders {
		paths = append(paths, folder.Path)
	}
	want := []string{"Inbox", "Projects/Garden", "Projects/Garden/Raised beds", "Work", "Work/Clients"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %q, want %q", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %q, want %q", paths, want)
		}
	}
}

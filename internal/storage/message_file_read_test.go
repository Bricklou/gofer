package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/store"
)

// Bodies are saved compressed; the reader gets them back as they were, and
// bodies saved before compression keep working.
func TestEmailBodyReadsCompressedAndPlainFiles(t *testing.T) {
	ctx := context.Background()
	db, err := New(filepath.Join(t.TempDir(), "bodies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Write().Exec(`INSERT INTO users(id, username, username_normalized) VALUES ('ana', 'ana', 'ana');
		INSERT INTO accounts(id, user_id, provider, email_address, display_name, color, initials) VALUES ('acc', 'ana', 'imap', 'ana@example.test', 'Ana', '#336699', 'A');
		INSERT INTO messages(id, account_id, from_email) VALUES (1, 'acc', 'ben@example.test'), (2, 'acc', 'ben@example.test')`); err != nil {
		t.Fatal(err)
	}

	blobs := store.NewBlobStore(t.TempDir())
	html := []byte(strings.Repeat("<p>The garden committee meets on Friday at six.</p>\n", 300))
	compressedPath, err := blobs.StoreBodyHTML(ctx, "acc", 1, html)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(compressedPath); err != nil || info.Size() >= int64(len(html))/4 {
		t.Fatalf("body was not saved compressed: %v, %v", info, err)
	}
	plainPath := filepath.Join(t.TempDir(), "body.html")
	if err := os.WriteFile(plainPath, html, 0o644); err != nil {
		t.Fatal(err)
	}
	for id, path := range map[string]string{"1": compressedPath, "2": plainPath} {
		if _, err := db.Write().Exec(`UPDATE messages SET body_html_path = ? WHERE id = ?`, path, id); err != nil {
			t.Fatal(err)
		}
		body, err := db.GetEmailBodyForUser(ctx, id, "ana")
		if err != nil || !bytes.Equal(body, html) {
			t.Fatalf("message %s: got %d bytes, err %v; want the %d byte body", id, len(body), err, len(html))
		}
	}
}

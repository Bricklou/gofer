package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type compressFixture struct {
	root    string
	files   map[string][]byte
	before  map[string][]byte
	content map[string][]byte
}

// newCompressFixture lays out message files as an older Gofer saved them:
// plain bodies and raw messages, an attachment, a file already compressed and
// a leftover from an interrupted conversion.
func newCompressFixture(t *testing.T) compressFixture {
	t.Helper()
	root := t.TempDir()
	random := make([]byte, 4096)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	newer := []byte(strings.Repeat("<p>Saved after compression arrived.</p>", 100))
	files := map[string][]byte{
		"acc/messages/1/v1/raw.eml":                 []byte("From: Ana <ana@example.test>\r\nSubject: Rota\r\n\r\n" + strings.Repeat("Ben covers Monday, Ana covers Tuesday.\r\n", 100)),
		"acc/messages/1/v1/body.txt":                []byte(strings.Repeat("Ben covers Monday, Ana covers Tuesday.\n", 100)),
		"acc/messages/1/v1/body.html":               []byte(strings.Repeat("<p>Ben covers Monday, Ana covers Tuesday.</p>", 100)),
		"acc/messages/1/v1/body_original.html":      []byte(strings.Repeat("<p style=\"color:red\">Ben covers Monday.</p>", 100)),
		"acc/messages/2/v1/body.txt":                []byte("See you at noon."),
		"acc/messages/3/v1/body.txt":                random,
		"acc/messages/4/v1/body.html":               encodeMessageFile(newer),
		"acc/messages/1/v1/attachments/1-notes.txt": []byte(strings.Repeat("attachment text ", 200)),
		"acc/messages/1/v1/.compress-123":           []byte("leftover"),
	}
	before := map[string][]byte{}
	content := map[string][]byte{}
	for name, data := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o640); err != nil {
			t.Fatal(err)
		}
		before[name] = data
		content[name] = data
	}
	content["acc/messages/4/v1/body.html"] = newer
	return compressFixture{root: root, files: files, before: before, content: content}
}

func (f compressFixture) stored(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCompressMessageFilesConvertsOlderFiles(t *testing.T) {
	f := newCompressFixture(t)
	report, err := CompressMessageFiles(context.Background(), f.root, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := CompressReport{Directory: f.root, Scanned: 7, Compressed: 4, AlreadyCompressed: 1, LeftAsIs: 2, LeftoversRemoved: 1}
	report.BytesBefore, report.BytesAfter = 0, 0
	if report != want {
		t.Fatalf("report = %+v\nwant     %+v", report, want)
	}
	for _, name := range []string{"acc/messages/1/v1/raw.eml", "acc/messages/1/v1/body.txt", "acc/messages/1/v1/body.html", "acc/messages/1/v1/body_original.html"} {
		stored := f.stored(t, name)
		if !bytes.HasPrefix(stored, zstdMagic) || len(stored) >= len(f.before[name]) {
			t.Fatalf("%s was not compressed", name)
		}
		read, err := ReadMessageFile(filepath.Join(f.root, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(read, f.content[name]) {
			t.Fatalf("%s reads back differently (err %v)", name, err)
		}
		info, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(name)))
		if err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("%s permissions changed: %v %v", name, info.Mode(), err)
		}
	}
	for _, name := range []string{"acc/messages/2/v1/body.txt", "acc/messages/3/v1/body.txt", "acc/messages/4/v1/body.html", "acc/messages/1/v1/attachments/1-notes.txt"} {
		if !bytes.Equal(f.stored(t, name), f.before[name]) {
			t.Fatalf("%s should have been left as it was", name)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "acc/messages/1/v1/.compress-123")); !os.IsNotExist(err) {
		t.Fatalf("leftover from an interrupted run is still there: %v", err)
	}

	again, err := CompressMessageFiles(context.Background(), f.root, false, nil)
	if err != nil || again.Compressed != 0 || again.AlreadyCompressed != 5 {
		t.Fatalf("second run = %+v, %v; want nothing left to compress", again, err)
	}
}

func TestCompressMessageFilesDryRunChangesNothing(t *testing.T) {
	f := newCompressFixture(t)
	report, err := CompressMessageFiles(context.Background(), f.root, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.Compressed != 4 || report.BytesAfter >= report.BytesBefore || report.LeftoversRemoved != 0 {
		t.Fatalf("dry run report = %+v", report)
	}
	for name, data := range f.before {
		if !bytes.Equal(f.stored(t, name), data) {
			t.Fatalf("dry run changed %s", name)
		}
	}
}

func TestCompressMessageFilesRefusesAMissingDirectory(t *testing.T) {
	if _, err := CompressMessageFiles(context.Background(), filepath.Join(t.TempDir(), "missing"), false, nil); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}

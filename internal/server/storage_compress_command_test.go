package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/store"
)

func writeUncompressedBodies(t *testing.T, root string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		dir := filepath.Join(root, "acc", "messages", string(rune('a'+i)), "v1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte(strings.Repeat("<p>Ana and Ben will bring the projector.</p>", 100))
		if err := os.WriteFile(filepath.Join(dir, "body.html"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStorageCompressCommandReportsProgressAndJSON(t *testing.T) {
	root := t.TempDir()
	writeUncompressedBodies(t, root, 3)
	interval := compressProgressInterval
	compressProgressInterval = 0
	t.Cleanup(func() { compressProgressInterval = interval })

	var stdout, stderr bytes.Buffer
	handled, code := RunStorageCommand(context.Background(), []string{"storage", "compress", "--dir", root}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("handled %t, exit %d, stderr:\n%s", handled, code, stderr.String())
	}
	var report store.CompressReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || report.Compressed != 3 {
		t.Fatalf("stdout is not the JSON report (%v): %s", err, stdout.String())
	}
	out := stderr.String()
	for _, want := range []string{"Compressing message files in " + root, "  checked 1 file, compressed 1,", "  checked 3 files, compressed 3,", "Done: checked 3 files, compressed 3, 12.9 KB →"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stderr is missing %q:\n%s", want, out)
		}
	}
}

func TestStorageCompressCommandSaysHowToContinueAfterAnInterruption(t *testing.T) {
	root := t.TempDir()
	writeUncompressedBodies(t, root, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	_, code := RunStorageCommand(ctx, []string{"storage", "compress", "--dir", root}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "Interrupted: checked 0 files") || !strings.Contains(stderr.String(), "run the command again to continue") {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr.String())
	}
}

func TestFormatCountAndBytes(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := formatCount(n); got != want {
			t.Fatalf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int64]string{512: "512 bytes", 2048: "2.0 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB"} {
		if got := formatBytes(n); got != want {
			t.Fatalf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

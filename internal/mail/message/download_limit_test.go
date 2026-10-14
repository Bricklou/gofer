package message

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cristianadrielbraun/gofer/internal/store"
)

// A saved message is compressed on disk, so the download limit must apply to
// the message itself, not to the smaller file that holds it.
func TestReadSavedMessageAppliesTheLimitToTheMessageNotTheFile(t *testing.T) {
	blobs := store.NewBlobStore(t.TempDir())
	raw := []byte("From: Ana <ana@example.test>\r\nSubject: Minutes\r\n\r\n" + strings.Repeat("The team agreed on the plan for next week.\r\n", 2000))
	path, err := blobs.StoreRaw(context.Background(), "acc", 3, raw)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() >= int64(len(raw))/4 {
		t.Fatalf("raw message was not saved compressed: %v, %v", info, err)
	}

	read, err := ReadSavedMessage(context.Background(), path)
	if err != nil || !bytes.Equal(read, raw) {
		t.Fatalf("read %d bytes, err %v; want the %d byte message", len(read), err, len(raw))
	}

	limit := int64(len(raw)) - 1
	read, err = ReadSavedMessage(WithDownloadLimit(context.Background(), limit), path)
	var tooLarge *TooLargeError
	if read != nil || !errors.As(err, &tooLarge) || tooLarge.Size != int64(len(raw)) || tooLarge.Limit != limit {
		t.Fatalf("over the limit: read %d bytes, err %v; want TooLargeError for %d bytes", len(read), err, len(raw))
	}
}

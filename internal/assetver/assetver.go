// Package assetver adds a content version to asset URLs, so browsers can
// keep an asset for as long as it does not change and fetch it again as soon
// as a new release changes it.
package assetver

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const prefix = "/assets"

type entry struct {
	modTime time.Time
	size    int64
	version string
}

var (
	source atomic.Pointer[http.FileSystem]
	cache  sync.Map // path -> entry
)

// Use sets the file system the assets are served from, rooted at /assets.
func Use(fs http.FileSystem) {
	source.Store(&fs)
	cache.Clear()
}

// Version returns the content version of an asset path such as
// "/assets/js/app.js", or false when it is not a known file.
func Version(path string) (string, bool) {
	fsPointer := source.Load()
	if fsPointer == nil || !strings.HasPrefix(path, prefix+"/") {
		return "", false
	}
	file, err := (*fsPointer).Open(strings.TrimPrefix(path, prefix))
	if err != nil {
		return "", false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		return "", false
	}
	// Embedded files never change; files on disk are re-read when they do.
	if cached, ok := cache.Load(path); ok {
		e := cached.(entry)
		if e.modTime.Equal(info.ModTime()) && e.size == info.Size() {
			return e.version, true
		}
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", false
	}
	version := hex.EncodeToString(hash.Sum(nil))[:12]
	cache.Store(path, entry{modTime: info.ModTime(), size: info.Size(), version: version})
	return version, true
}

// URL returns the asset path with its content version, for pages to link.
// An unknown path is returned unchanged.
func URL(path string) string {
	if version, ok := Version(path); ok {
		return path + "?v=" + version
	}
	return path
}

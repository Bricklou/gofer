package handler

import (
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Files taken from a message (attachments, inline parts, saved remote images)
// are chosen by the sender but served from the app's own origin. Opened
// directly in a tab, an HTML or SVG part would run with the reader's session,
// so every such response is sandboxed, is never type-sniffed, and is shown in
// place only when it is an image. Everything else is downloaded.
const mailFileContentSecurityPolicy = "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; frame-ancestors 'self'; sandbox"

var mailFileRasterTypes = map[string]bool{
	"image/png":                true,
	"image/jpeg":               true,
	"image/gif":                true,
	"image/webp":               true,
	"image/avif":               true,
	"image/bmp":                true,
	"image/x-icon":             true,
	"image/vnd.microsoft.icon": true,
}

var remoteAssetTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".bmp":  "image/bmp",
}

func mailFileMediaType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "application/octet-stream"
	}
	if mediaType == "image/jpg" {
		return "image/jpeg"
	}
	return mediaType
}

// remoteAssetContentType names a saved remote image by the extension it was
// stored with. Files saved without one were not recognized as images.
func remoteAssetContentType(filename string) string {
	if contentType, ok := remoteAssetTypes[strings.ToLower(filepath.Ext(filename))]; ok {
		return contentType
	}
	return "application/octet-stream"
}

// mailFileDisplaysInline reports whether a message file may be shown in place.
// Raster images can't run code. SVG can, so it is shown only where the browser
// loads it as an image, such as an <img> in a message body, and never as a page.
func mailFileDisplaysInline(r *http.Request, mediaType string) bool {
	if mailFileRasterTypes[mediaType] {
		return true
	}
	return mediaType == "image/svg+xml" && r.Header.Get("Sec-Fetch-Dest") == "image"
}

// serveMailFile writes a file taken from a message. With inline set, images
// are shown in place; anything else, and every download, is sent as opaque
// bytes to save. The caller sets Cache-Control.
func serveMailFile(w http.ResponseWriter, r *http.Request, content io.ReadSeeker, filename, contentType string, inline bool) {
	mediaType := mailFileMediaType(contentType)
	disposition := "inline"
	if !inline || !mailFileDisplaysInline(r, mediaType) {
		disposition = "attachment"
		mediaType = "application/octet-stream"
	}
	header := w.Header()
	header.Set("Content-Type", mediaType)
	header.Set("Content-Security-Policy", mailFileContentSecurityPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Add("Vary", "Sec-Fetch-Dest")
	if value := mime.FormatMediaType(disposition, map[string]string{"filename": strings.TrimSpace(filename)}); value != "" && strings.TrimSpace(filename) != "" {
		header.Set("Content-Disposition", value)
	} else {
		header.Set("Content-Disposition", disposition)
	}
	http.ServeContent(w, r, "", time.Time{}, content)
}

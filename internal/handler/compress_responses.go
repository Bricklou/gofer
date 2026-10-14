package handler

import (
	"bytes"
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/cristianadrielbraun/gofer/internal/auth"
)

// Pages, list fragments, message views and JSON are compressed for browsers
// that accept gzip. A response is sent as it is when it is small, of another
// type, already encoded, or carries a CSRF token. Compressing a secret next to
// text an attacker controls, such as a subject line, can reveal the secret
// through the response size (BREACH), so token-bearing responses and the
// pages that show other secrets are never compressed.
const (
	compressMinBytes = 1024
	// compressMaxBuffer bounds what a response may buffer before it is sent
	// uncompressed as it is written.
	compressMaxBuffer = 8 << 20
)

// compressExcludedPrefixes serve secrets, streams or files of their own.
var compressExcludedPrefixes = []string{
	"/assets/", "/api/events", "/settings/security", "/admin", "/api/admin",
	"/auth/", "/setup", "/account/",
}

var compressibleResponseTypes = map[string]bool{
	"text/html": true, "application/json": true, "text/plain": true,
}

var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, 5)
	return w
}}

// CompressResponses gzips eligible responses; see the rules above. It must
// wrap the authentication middleware so token issuance is tracked.
func CompressResponses(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !acceptsGzip(r.Header.Get("Accept-Encoding")) || compressExcluded(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, issued := auth.TrackCSRFTokens(r.Context())
		cw := &compressWriter{ResponseWriter: w}
		next.ServeHTTP(cw, r.WithContext(ctx))
		cw.finish(issued())
	})
}

func compressExcluded(path string) bool {
	for _, prefix := range compressExcludedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// compressWriter holds the response until the handler finishes, when it is
// known whether a token was issued. A response of another type, or a very
// large body, is sent uncompressed as it is written.
type compressWriter struct {
	http.ResponseWriter
	status      int
	buffer      bytes.Buffer
	passthrough bool
}

func (c *compressWriter) WriteHeader(status int) {
	if c.passthrough {
		c.ResponseWriter.WriteHeader(status)
		return
	}
	if c.status == 0 {
		c.status = status
	}
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if c.passthrough {
		return c.ResponseWriter.Write(p)
	}
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.buffer.Len() == 0 && !c.mayCompress() {
		// Downloads and other types stream as before, without buffering.
		if err := c.startPassthrough(); err != nil {
			return 0, err
		}
		return c.ResponseWriter.Write(p)
	}
	if c.buffer.Len()+len(p) > compressMaxBuffer {
		if err := c.startPassthrough(); err != nil {
			return 0, err
		}
		return c.ResponseWriter.Write(p)
	}
	return c.buffer.Write(p)
}

// mayCompress reports whether the headers set so far allow compression. An
// unset type is decided at the end, from what the handler set by then.
func (c *compressWriter) mayCompress() bool {
	header := c.ResponseWriter.Header()
	if header.Get("Content-Encoding") != "" {
		return false
	}
	contentType := header.Get("Content-Type")
	if contentType == "" {
		return true
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	return compressibleResponseTypes[mediaType]
}

// Flush sends the response so far when it will not be compressed. While a
// page or JSON is being collected for compression, a flush waits for the end:
// templ flushes after every render. Streams use their own types, such as
// text/event-stream or application/x-ndjson, and are passed through.
func (c *compressWriter) Flush() {
	if !c.passthrough {
		if c.mayCompress() {
			return
		}
		_ = c.startPassthrough()
	}
	if flusher, ok := c.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c *compressWriter) startPassthrough() error {
	c.passthrough = true
	if c.status != 0 {
		c.ResponseWriter.WriteHeader(c.status)
	}
	if c.buffer.Len() == 0 {
		return nil
	}
	_, err := c.ResponseWriter.Write(c.buffer.Bytes())
	c.buffer.Reset()
	return err
}

func (c *compressWriter) finish(tokenIssued bool) {
	if c.passthrough {
		return
	}
	header := c.ResponseWriter.Header()
	mediaType, _, _ := mime.ParseMediaType(header.Get("Content-Type"))
	compressible := compressibleResponseTypes[mediaType]
	if compressible {
		header.Add("Vary", "Accept-Encoding")
	}
	if !compressible || tokenIssued || c.buffer.Len() < compressMinBytes || header.Get("Content-Encoding") != "" ||
		c.status == http.StatusNoContent || c.status == http.StatusNotModified {
		_ = c.startPassthrough()
		return
	}
	var compressed bytes.Buffer
	gz := gzipWriters.Get().(*gzip.Writer)
	gz.Reset(&compressed)
	_, err := gz.Write(c.buffer.Bytes())
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	gzipWriters.Put(gz)
	if err != nil {
		_ = c.startPassthrough()
		return
	}
	header.Set("Content-Encoding", "gzip")
	header.Set("Content-Length", strconv.Itoa(compressed.Len()))
	c.ResponseWriter.WriteHeader(c.status)
	_, _ = c.ResponseWriter.Write(compressed.Bytes())
}

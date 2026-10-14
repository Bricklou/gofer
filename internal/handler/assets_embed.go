//go:build embedded_assets

package handler

import (
	"net/http"
	"path"
	"strings"

	appassets "github.com/cristianadrielbraun/gofer/assets"
)

func assetFileSystem() http.FileSystem {
	return minifiedScripts{http.FS(appassets.FS)}
}

// minifiedScripts serves the minified copy of a script that `task generate`
// wrote to min/js, under the script's own path, and every other file as it is.
type minifiedScripts struct{ base http.FileSystem }

func (m minifiedScripts) Open(name string) (http.File, error) {
	name = path.Clean("/" + name)
	if strings.HasPrefix(name, "/js/") && path.Ext(name) == ".js" {
		if file, err := m.base.Open("/min" + name); err == nil {
			return file, nil
		}
	}
	return m.base.Open(name)
}

func serveServiceWorker(w http.ResponseWriter, r *http.Request) {
	http.ServeFileFS(w, r, appassets.FS, "js/sw.js")
}

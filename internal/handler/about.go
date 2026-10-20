package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/cristianadrielbraun/gofer/internal/attribution"
	"github.com/cristianadrielbraun/gofer/internal/buildinfo"
	"github.com/cristianadrielbraun/gofer/internal/updatecheck"
	"github.com/cristianadrielbraun/gofer/internal/views"
)

// handleAboutUpdateCheck asks GitHub whether a newer Gofer exists, when
// someone presses the button on the About page. It only reports.
func (h *Handler) handleAboutUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "text/html")
	result, err := updatecheck.Default.Check(ctx, buildinfo.Get())
	if err != nil {
		log.Printf("update check: %v", err)
		msg := "Could not check for updates. Try again later."
		if errors.Is(err, updatecheck.ErrRateLimited) {
			msg = "GitHub is limiting requests right now. Try again later."
		}
		views.AboutUpdateCheck(nil, msg).Render(ctx, w)
		return
	}
	views.AboutUpdateCheck(&result, "").Render(ctx, w)
}

// handleAboutLicenses serves the full license texts of the third-party
// software built into Gofer.
func (h *Handler) handleAboutLicenses(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(attribution.Notices()))
}

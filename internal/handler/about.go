package handler

import (
	"errors"
	"log"
	"net/http"

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

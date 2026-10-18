package server

import (
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/cristianadrielbraun/gofer/internal/browser"
)

// openBrowserIfEnabled opens baseURL in the desktop's browser when
// GOFER_OPEN_BROWSER is true. It is off by default, so servers and containers
// never try. Call it once the listener is open, so the page loads at once.
// A browser that cannot be opened is logged and never stops Gofer.
func openBrowserIfEnabled(baseURL string) {
	raw := strings.TrimSpace(os.Getenv("GOFER_OPEN_BROWSER"))
	if raw == "" {
		return
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		log.Printf("GOFER_OPEN_BROWSER must be true or false; not opening a browser")
		return
	}
	if !enabled {
		return
	}
	logFailure := func(err error) {
		if err != nil {
			log.Printf("could not open a browser on %s: %v", baseURL, err)
		}
	}
	logFailure(browser.Open(baseURL, logFailure))
}

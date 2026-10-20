package providers

import (
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ErrJMAPDisabled refuses JMAP accounts while the experiment is off.
var ErrJMAPDisabled = errors.New("JMAP accounts are experimental and turned off; set GOFER_JMAP_EXPERIMENT=true to use them")

var jmapFlagWarning sync.Once

// JMAPEnabled reports whether GOFER_JMAP_EXPERIMENT turns on JMAP accounts.
// They are off by default while JMAP support is incomplete.
func JMAPEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("GOFER_JMAP_EXPERIMENT"))
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		jmapFlagWarning.Do(func() { log.Printf("GOFER_JMAP_EXPERIMENT must be true or false; JMAP accounts stay off") })
		return false
	}
	return enabled
}

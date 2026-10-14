package selectbox

import (
	"encoding/json"

	"github.com/a-h/templ"
)

// LazyItem is one entry of a menu whose entries are built in the browser.
type LazyItem struct {
	Value string
	Label string
}

// LazyItems returns the Group attributes for a long menu that the page sends
// with a single entry, the selected one or else the first. selectbox.js copies
// that entry into every item listed here, in this order, when the menu is
// first used or a script sets a value it does not have yet.
func LazyItems(items []LazyItem) templ.Attributes {
	pairs := make([][2]string, len(items))
	for i, item := range items {
		pairs[i] = [2]string{item.Value, item.Label}
	}
	encoded, _ := json.Marshal(pairs)
	return templ.Attributes{"data-tui-selectbox-lazy-items": string(encoded)}
}

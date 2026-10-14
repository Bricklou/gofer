package views

import (
	"fmt"

	"github.com/cristianadrielbraun/gofer/components/selectbox"
)

// scheduleTimeItems lists the two-digit values from 0 up to (not including)
// limit in steps of step, for the scheduled-send hour and minute menus.
func scheduleTimeItems(limit, step int) []selectbox.LazyItem {
	var items []selectbox.LazyItem
	for value := 0; value < limit; value += step {
		text := fmt.Sprintf("%02d", value)
		items = append(items, selectbox.LazyItem{Value: text, Label: text})
	}
	return items
}

package providers

import "testing"

func TestJMAPEnabled(t *testing.T) {
	for value, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true, " TRUE ": true, "yes": false} {
		t.Setenv("GOFER_JMAP_EXPERIMENT", value)
		if got := JMAPEnabled(); got != want {
			t.Errorf("GOFER_JMAP_EXPERIMENT=%q: JMAPEnabled() = %v, want %v", value, got, want)
		}
	}
}

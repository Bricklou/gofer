package attribution

import (
	"strings"
	"testing"
)

func TestListsAreComplete(t *testing.T) {
	bundled := Bundled()
	if len(bundled) == 0 || bundled[0].Name != "Go standard library" || !strings.HasPrefix(bundled[0].Version, "go") {
		t.Fatalf("bundled list should start with the Go standard library and its version: %+v", bundled)
	}
	modules := GoModules()
	if len(modules) == 0 {
		t.Fatal("no Go modules listed; run scripts/licenses.sh")
	}
	seen := map[string]bool{}
	for _, lib := range append(bundled, modules...) {
		if lib.Name == "" || lib.License == "" || lib.URL == "" {
			t.Errorf("incomplete entry %+v", lib)
		}
		if seen[lib.Name] {
			t.Errorf("%s listed twice", lib.Name)
		}
		seen[lib.Name] = true
		if !strings.Contains(Notices(), "\n"+lib.Name) {
			t.Errorf("notices have no section for %s", lib.Name)
		}
	}
}

package web

import (
	"os/exec"
	"strings"
	"testing"
)

// xf web gets the new features and the TUI only fixes (see the README), so
// xf web must not lean on the TUI's code: what both share is in package
// frontend.
func TestNoTUIDependency(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, p := range strings.Fields(string(out)) {
		if p == "github.com/ashish10alex/xf/internal/ui" || strings.HasPrefix(p, "charm.land/") {
			t.Errorf("xf web depends on %s, the TUI's; move what it needs to package frontend", p)
		}
	}
}

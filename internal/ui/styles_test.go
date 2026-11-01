package ui

import (
	"strings"
	"testing"
)

// TestAutoThemeTextIsTheTerminals checks auto draws plain text in the
// terminal's own foreground, and a forced theme in its own colour.
func TestAutoThemeTextIsTheTerminals(t *testing.T) {
	for _, dark := range []bool{false, true} {
		if got := themeStyles("auto", dark).text.Render("name"); got != "name" {
			t.Errorf("auto, dark %v: text %q, want no colour", dark, got)
		}
		if got := themeStyles("auto", dark).selected.Render("name"); strings.Contains(got, "38;") {
			t.Errorf("auto, dark %v: selected %q has a foreground", dark, got)
		}
	}
	for _, theme := range []string{"light", "dark"} {
		if got := themeStyles(theme, true).text.Render("name"); !strings.Contains(got, "38;") {
			t.Errorf("%s: text %q, want its own colour", theme, got)
		}
	}
}

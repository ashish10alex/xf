package dataform

import (
	"slices"
	"strings"
	"testing"
)

// The example compiled on Windows, where Dataform prints backslash paths,
// gives the same files as on macOS: every action and compile error names
// its file with forward slashes.
func TestWindowsCompileOutput(t *testing.T) {
	for _, f := range []string{"dataform", "dataform-broken"} {
		t.Run(f, func(t *testing.T) {
			mac, win := loadGraph(t, "examples/"+f+".json"), loadGraph(t, "examples/"+f+"-windows.json")
			if len(win.Actions) != len(mac.Actions) {
				t.Fatalf("%d actions on Windows, %d on macOS", len(win.Actions), len(mac.Actions))
			}
			for id, a := range mac.Actions {
				w := win.Actions[id]
				if w == nil || w.FileName != a.FileName {
					got := "<missing>"
					if w != nil {
						got = w.FileName
					}
					t.Errorf("%s: file %q on Windows, %q on macOS", id, got, a.FileName)
					continue
				}
				if len(win.ActionsInFile(a.FileName)) != len(mac.ActionsInFile(a.FileName)) {
					t.Errorf("actions in %s differ", a.FileName)
				}
			}
			var macErrs, winErrs []string
			for _, e := range mac.Errors {
				macErrs = append(macErrs, e.FileName)
			}
			for _, e := range win.Errors {
				winErrs = append(winErrs, e.FileName)
				if strings.Contains(e.FileName, `\`) {
					t.Errorf("error file %q has backslashes", e.FileName)
				}
			}
			if !slices.Equal(winErrs, macErrs) {
				t.Errorf("error files %q on Windows, %q on macOS", winErrs, macErrs)
			}
		})
	}
}

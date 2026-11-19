package dataform

import (
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// The example compiled on Windows gives the same files as on macOS: every
// action and compile error names its file with forward slashes, though
// Dataform CLIs before 3.1 print backslashes there (the broken example's
// errors still do).
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

// Dataform CLIs before 3.1 print backslash paths on Windows (3.1.2, which
// compiled dataform-windows.json, prints forward slashes): an action's file
// reads with forward slashes either way.
func TestBackslashFileNames(t *testing.T) {
	g, err := ParseCompileJSON([]byte(`{"tables":[{"type":"view","target":{"database":"p","schema":"s","name":"v"},"fileName":"definitions\\staging\\v.sqlx"}],
		"propertyGraphs":[{"target":{"database":"p","schema":"s","name":"g"},"fileName":"definitions\\graphs\\graph.yaml"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"p.s.v": "definitions/staging/v.sqlx", "p.s.g": "definitions/graphs/graph.yaml"} {
		if a := g.Actions[project.ActionID(id)]; a == nil || a.FileName != want {
			t.Errorf("%s: got %+v, want file %s", id, a, want)
		}
	}
}

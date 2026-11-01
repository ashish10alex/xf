package web

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// Pane 2's errors tab groups a failing compile's errors by kind and takes
// pane 1 to each one's line, by an action of its file in the last good
// compile; it says when the compile failed and how long it took.
func TestErrorsView(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "definitions", "orders.sqlx")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("config { type: \"table\" }\nSELECT id\nFROM ${ref(\"nope\")}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := newFakeEngine(testGraph())
	s, ts, c := newTest(t, frontend.Deps{Engine: eng, Root: root, Info: &frontend.ProjectInfo{ToolBin: "/bin/dataform"}})
	g := s.only().graph()
	eng.states <- engine.State{Graph: g, Compiles: 2, Stale: true, Started: time.Now().Add(-1500 * time.Millisecond),
		Errors: []project.CompilationError{
			{FileName: "definitions/orders.sqlx", Message: `Could not resolve "nope"`},
			{FileName: "definitions/gone.sqlx", Message: "Unexpected identifier 'bigquery'",
				Stack: "/tmp/x/definitions/gone.sqlx:14\n  bigquery: {\n  ^^^^^^^^\n\nSyntaxError: Unexpected identifier 'bigquery'\n    at new Script (node:vm:1:1)"},
		}}
	deadline := time.Now().Add(5 * time.Second)
	for !s.only().hasErrors() {
		if time.Now().After(deadline) {
			t.Fatal("the failed compile never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var v struct {
		Bin    string
		At     time.Time
		Took   float64
		Stale  bool
		Errors []struct {
			File, Kind, Snippet, Action string
			Line                        int
		}
	}
	getJSON(t, c, ts.URL+"/api/errors", &v)
	if v.Bin != "/bin/dataform" || v.At.IsZero() || v.Took < 1 || !v.Stale || len(v.Errors) != 2 {
		t.Fatalf("errors = %+v", v)
	}
	if e := v.Errors[0]; e.Kind != "unresolved" || e.Line != 3 || e.Action != "p.mart.orders" {
		t.Errorf("the unresolved ref = %+v, want on line 3 by p.mart.orders", e)
	}
	if e := v.Errors[1]; e.Kind != "root" || e.Line != 14 || e.Action != "" || e.Snippet == "" {
		t.Errorf("the syntax error = %+v, want a root error on line 14 with its code and no action", e)
	}
}

// dataform install is offered only for a project that has a package.json:
// without one, workflow_settings.yaml's dataformCoreVersion is the fix.
func TestCoreInstallNeedsAPackageJSON(t *testing.T) {
	msg := "Error: @dataform/core 0.0.0 is not compatible with @dataform/cli 3.1.2. ... pin @dataform/core in package.json and run `dataform install`."
	root := t.TempDir()
	p := &projectState{tool: "dataform", deps: frontend.Deps{Root: root}}
	p.st.Err = errors.New(msg)
	if f := p.fixLocked(false); f != nil {
		t.Errorf("fix without a package.json = %+v, want none", f)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := p.fixLocked(false); f == nil || !f.core || !strings.HasSuffix(f.Command, " install") {
		t.Errorf("fix with a package.json = %+v, want dataform install", f)
	}
	p.tool = "dbt"
	if f := p.fixLocked(false); f != nil {
		t.Errorf("fix for dbt = %+v, want none", f)
	}
}

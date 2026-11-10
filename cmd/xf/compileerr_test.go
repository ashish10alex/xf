package main

import (
	"errors"
	"testing"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
)

func TestCompileError(t *testing.T) {
	other := errors.New("permission denied")
	cases := []struct {
		name string
		err  error
		st   engine.State
		want string
	}{
		{"other errors pass through", other, engine.State{Err: errors.New("x")}, "permission denied"},
		{"the compiler failed: its first two lines, not the stack", engine.ErrStale,
			engine.State{Err: errors.New("dataform compile --json: exit status 1\nDataform encountered an error: Could not find @dataform/core.\nError: Could not find @dataform/core.\n    at compile$1 (worker_bundle.js:1:1)")},
			"the project doesn't compile: dataform compile --json: exit status 1 — Dataform encountered an error: Could not find @dataform/core."},
		{"one compile error", engine.ErrStale,
			engine.State{Errors: []project.CompilationError{{FileName: "definitions/a.sqlx", Message: "Unexpected token"}}},
			"the project has compile errors: definitions/a.sqlx: Unexpected token"},
		{"several, the first with no file", engine.ErrStale,
			engine.State{Errors: []project.CompilationError{{Message: "Duplicate action name"}, {FileName: "b.sqlx", Message: "x"}}},
			"the project has compile errors (2): Duplicate action name"},
		{"nothing known", errors.New(engine.ErrStale.Error()), engine.State{}, "the project has compile errors"},
	}
	for _, c := range cases {
		if got := compileError(c.err, c.st).Error(); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	if compileError(nil, engine.State{}) != nil {
		t.Error("nil isn't an error")
	}
}

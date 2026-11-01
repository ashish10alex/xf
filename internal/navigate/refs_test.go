package navigate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// TestNamedAt finds the line naming each declaration of a sources.js and
// each source table of a sources.yml, and says so when no line does.
func TestNamedAt(t *testing.T) {
	files := map[string]string{
		"/p/definitions/sources.js": "// sources\n[\n  \"ACCOUNTS_V\"\n, 'ADDRESSES_V'\n].forEach((n) => declare({ schema: \"crm\", name: n }));\n" +
			"[\"X\"].forEach((n) => declare({ schema: \"crm\", name: \"pre_\" + n }));\n",
		"/p/models/sources.yml": "sources:\n  - name: raw\n    tables:\n      - name: orders\n      - name: \"customers\"\n  - name: other\n    tables:\n      - name: payments\n",
	}
	p := Project{Root: "/p", ReadFile: func(path string) ([]byte, error) {
		if s, ok := files[filepath.ToSlash(path)]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}}
	decl := func(name string) *project.Action {
		target := project.Target{Database: "d", Schema: "crm", Name: name}
		return &project.Action{Kind: project.KindDeclaration, Target: target, CanonicalTarget: target, FileName: "definitions/sources.js"}
	}
	source := func(src, name string) *project.Action {
		return &project.Action{Kind: project.KindSource, Target: project.Target{Schema: src, Name: name}, FileName: "models/sources.yml",
			DbtName: &project.DbtName{Source: src, Name: name}}
	}
	for _, c := range []struct {
		a    *project.Action
		line int
		ok   bool
	}{
		{decl("ACCOUNTS_V"), 2, true},
		{decl("ADDRESSES_V"), 3, true},
		{decl("pre_X"), 0, false}, // the loop builds the name
		{source("raw", "orders"), 3, true},
		{source("raw", "customers"), 4, true},
		{source("other", "payments"), 7, true},
		{source("raw", "payments"), 1, false}, // listed under another source
	} {
		loc, ok := NamedAt(p, c.a)
		if loc.Line != c.line || ok != c.ok {
			t.Errorf("%s: got line %d, %v; want %d, %v", c.a.Target.Name, loc.Line, ok, c.line, c.ok)
		}
	}
}

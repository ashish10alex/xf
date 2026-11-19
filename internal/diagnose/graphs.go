package diagnose

import (
	"io/fs"
	"path"
	"path/filepath"

	"github.com/ashish10alex/xf/internal/project"
)

// GraphCLIVersion is the first @dataform/cli that compiles a property
// graph's definitions/**/graph.yaml. Older ones leave the file out of the
// compile without an error, and a ref() to the graph still resolves, to an
// action the compile doesn't have. (A @dataform/core too old for the file
// fails on it, with a compile error.)
const GraphCLIVersion = "3.0.65"

// skippedGraph is the warning on a graph.yaml that a compile left out.
const skippedGraph = "This property graph isn't in the compile: dataform CLIs before " + GraphCLIVersion +
	" skip graph.yaml without an error. Upgrade the CLI: npm i -g @dataform/cli@latest"

// SkippedGraphs are warnings on the property graph files of a Dataform
// project that g, a clean compile of it, has no action from. fsys is the
// project's root; the files are its definitions/**/graph.yaml, in the
// order fs.WalkDir visits them (lexical). Only a compile without errors
// tells: a failed one may hold an older graph.
func SkippedGraphs(fsys fs.FS, g *project.Graph) []project.CompilationError {
	if g == nil || g.HasErrors() {
		return nil
	}
	compiled := map[string]bool{}
	for _, a := range g.Actions {
		if a.Kind == project.KindPropertyGraph {
			compiled[a.FileName] = true
		}
	}
	var out []project.CompilationError
	_ = fs.WalkDir(fsys, "definitions", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil // unreadable, or no definitions/: nothing to say
		case d.IsDir() && d.Name() == "node_modules":
			return fs.SkipDir
		case !d.IsDir() && path.Base(p) == "graph.yaml" && !compiled[p]:
			out = append(out, project.CompilationError{FileName: p, Message: skippedGraph})
		}
		return nil
	})
	return out
}

// Warnings are the diagnostics for a compile's warnings, such as
// SkippedGraphs', by file: each on the file's first line, read with read.
func Warnings(root, tool string, warns []project.CompilationError, read func(string) ([]byte, error)) map[string][]Diagnostic {
	out := map[string][]Diagnostic{}
	for _, w := range warns {
		file := filepath.Join(root, filepath.FromSlash(w.FileName))
		src, _ := read(file)
		d := Diagnostic{File: file, End: len(lineText(src, 0)), Severity: Warning, Source: tool + " compile", Message: w.Message}
		out[file] = append(out[file], d)
	}
	return out
}

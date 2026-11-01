// Package navigate answers "where is the thing under the cursor defined?" for
// the files of a Dataform project (ref() and resolve() targets, includes
// names (file.Name), require() paths and CTEs) and of a dbt project (ref(), source()
// and macro calls in Jinja, and CTEs). It works on source text plus the
// compiled graph and has no editor or protocol dependencies.
package navigate

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/ashish10alex/xf/internal/project"
)

// Project is what Definition needs to know about the project being edited.
type Project struct {
	// Root is the absolute project root (the directory with workflow_settings.yaml).
	Root string
	// Graph is the last good compile, or nil when there is none yet. Without
	// it ref() and resolve() jumps find nothing; everything else still works.
	Graph *project.Graph
	// ReadFile reads a file by absolute path. An editor passes one that
	// prefers its open, possibly unsaved, buffers. Nil means os.ReadFile.
	ReadFile func(path string) ([]byte, error)
	// Dbt says the project is a dbt project, whose SQL calls ref(),
	// source() and macros in Jinja.
	Dbt bool
}

func (p Project) read(path string) ([]byte, error) {
	if p.ReadFile != nil {
		return p.ReadFile(path)
	}
	return os.ReadFile(path)
}

// Location is a place in a file.
type Location struct {
	// File is an absolute path.
	File string
	// Line is 0-based.
	Line int
	// Col is a byte offset within the line.
	Col int
}

// Definition returns where the symbol at byte offset in src is defined. file
// is src's absolute path. It returns nil when there is nothing to jump to.
func Definition(p Project, file string, src []byte, offset int) []Location {
	if offset < 0 || offset > len(src) {
		return nil
	}
	if p.Dbt {
		if locs, ok := dbtDefinition(p, file, src, offset); ok {
			return locs
		}
		return cteDefinition(file, src, offset)
	}
	if locs, ok := refDefinition(p, src, offset); ok {
		return locs
	}
	if locs, ok := requireDefinition(p, file, src, offset); ok {
		return locs
	}
	if inc, ok := IncludeAt(p, file, src, offset); ok {
		return []Location{inc.Location}
	}
	if !isJSFile(file) {
		return cteDefinition(file, src, offset)
	}
	return nil
}

// cteDefinition finds the CTE named at offset.
func cteDefinition(file string, src []byte, offset int) []Location {
	if name, ok := sqlNameAt(src, offset); ok {
		if c, ok := findCTEDefinition(src, name, offset); ok {
			return []Location{locationAt(file, src, c.NameStart)}
		}
	}
	return nil
}

// sqlNameAt returns the SQL identifier under offset: a word, or the inside
// of a `backtick quoted` name on the same line.
func sqlNameAt(src []byte, offset int) (string, bool) {
	lineStart := bytes.LastIndexByte(src[:offset], '\n') + 1
	lineEnd := len(src)
	if i := bytes.IndexByte(src[offset:], '\n'); i >= 0 {
		lineEnd = offset + i
	}
	var ticks []int
	for i := lineStart; i < lineEnd; i++ {
		if src[i] == '`' {
			ticks = append(ticks, i)
		}
	}
	for i := 0; i+1 < len(ticks); i += 2 {
		if open, close := ticks[i], ticks[i+1]; open < offset && offset <= close {
			return string(src[open+1 : close]), open+1 < close
		}
	}
	start, end := offset, offset
	for start > 0 && isWordByte(src[start-1]) {
		start--
	}
	for end < len(src) && isWordByte(src[end]) {
		end++
	}
	return string(src[start:end]), start < end
}

// locationAt converts a byte offset in src to a Location in file.
func locationAt(file string, src []byte, offset int) Location {
	lineStart := bytes.LastIndexByte(src[:offset], '\n') + 1
	return Location{File: file, Line: bytes.Count(src[:offset], []byte("\n")), Col: offset - lineStart}
}

func isJSFile(file string) bool {
	return filepath.Ext(file) == ".js"
}

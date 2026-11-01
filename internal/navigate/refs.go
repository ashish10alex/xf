package navigate

import (
	"bytes"
	"cmp"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// refCall matches ref(...) and resolve(...) calls, including ctx.ref(...) in
// JavaScript. Arguments never contain parentheses.
var refCall = regexp.MustCompile(`\b(?:ref|resolve)\s*\(([^()]*)\)`)

// stringLit matches a JavaScript string literal; one of the groups holds its
// contents.
var stringLit = regexp.MustCompile("\"([^\"\\\\]*)\"|'([^'\\\\]*)'|`([^`\\\\$]*)`")

// objectField matches `key: "value"` inside an object literal.
var objectField = regexp.MustCompile(`(\w+)\s*:\s*(?:"([^"]*)"|'([^']*)'|` + "`([^`$]*)`)")

// refTarget is what a ref() call asks for. Empty parts match anything.
type refTarget struct {
	Database, Schema, Name string
}

// refDefinition resolves a ref() or resolve() call around offset. ok is false
// when the cursor is not on one, so other lookups can have a go.
func refDefinition(p Project, src []byte, offset int) (locs []Location, ok bool) {
	args, _, _, found := refArgsAt(src, offset)
	if !found {
		return nil, false
	}
	want, valid := parseRefArgs(args)
	if !valid {
		return nil, true
	}
	for _, a := range refMatches(p, want) {
		loc := p.actionLocation(a)
		if !slices.Contains(locs, loc) {
			locs = append(locs, loc)
		}
	}
	return locs, true
}

// refMatches returns the actions a ref() asks for, ordered by file then id.
// It finds nothing without a graph.
func refMatches(p Project, want refTarget) []*project.Action {
	if p.Graph == nil {
		return nil
	}
	var matches []*project.Action
	for _, a := range p.Graph.Actions {
		if a.Kind != project.KindUnknown && a.FileName != "" && want.matches(a.CanonicalTarget) {
			matches = append(matches, a)
		}
	}
	slices.SortFunc(matches, func(x, y *project.Action) int {
		return cmp.Or(cmp.Compare(x.FileName, y.FileName), cmp.Compare(x.ID, y.ID))
	})
	return matches
}

// refArgsAt returns the argument text of the ref() or resolve() call that
// contains offset, and the call's byte range.
func refArgsAt(src []byte, offset int) (args string, start, end int, ok bool) {
	for _, m := range refCall.FindAllSubmatchIndex(src, -1) {
		if offset >= m[0] && offset <= m[1] {
			return string(src[m[2]:m[3]]), m[0], m[1], true
		}
		if m[0] > offset {
			break
		}
	}
	return "", 0, 0, false
}

// parseRefArgs understands ref("name"), ref("schema", "name"),
// ref("database", "schema", "name") and ref({database, schema, name}).
func parseRefArgs(args string) (refTarget, bool) {
	args = strings.TrimSpace(args)
	if strings.HasPrefix(args, "{") {
		var t refTarget
		for _, m := range objectField.FindAllStringSubmatch(args, -1) {
			v := m[2] + m[3] + m[4]
			switch m[1] {
			case "database":
				t.Database = v
			case "schema":
				t.Schema = v
			case "name":
				t.Name = v
			}
		}
		return t, t.Name != ""
	}
	var parts []string
	for _, m := range stringLit.FindAllStringSubmatch(args, -1) {
		parts = append(parts, m[1]+m[2]+m[3])
	}
	switch len(parts) {
	case 1:
		return refTarget{Name: parts[0]}, true
	case 2:
		return refTarget{Schema: parts[0], Name: parts[1]}, true
	case 3:
		return refTarget{Database: parts[0], Schema: parts[1], Name: parts[2]}, true
	}
	return refTarget{}, false
}

func (r refTarget) matches(t project.Target) bool {
	return r.Name == t.Name &&
		(r.Schema == "" || r.Schema == t.Schema) &&
		(r.Database == "" || r.Database == t.Database)
}

// ActionLocation is where an action is defined: the top of its sqlx file,
// or the line of its JavaScript file that names it.
func ActionLocation(p Project, a *project.Action) Location { return p.actionLocation(a) }

// actionLocation is where an action is defined: the top of a sqlx file, or
// the first line of a JavaScript file that mentions the action's name in
// quotes (its publish() or declare() call, say).
func (p Project) actionLocation(a *project.Action) Location {
	loc, _ := p.quotedAt(a)
	return loc
}

// NamedAt is the line of its file that names a: the name in quotes in a
// JavaScript file, a dbt source's `- name:` in its YAML file. ok is false
// when no line does (a name a loop builds) or a's file is neither.
func NamedAt(p Project, a *project.Action) (loc Location, ok bool) {
	if a.Kind == project.KindSource {
		return p.sourceAt(a)
	}
	return p.quotedAt(a)
}

// quotedAt is the first place a's JavaScript file has its name in quotes,
// ok false when it has none: the file's top.
func (p Project) quotedAt(a *project.Action) (Location, bool) {
	path := filepath.Join(p.Root, filepath.FromSlash(a.FileName))
	loc := Location{File: path}
	if !isJSFile(path) {
		return loc, false
	}
	src, err := p.read(path)
	if err != nil {
		return loc, false
	}
	name := a.CanonicalTarget.Name
	best := -1
	for _, q := range []string{`"`, `'`, "`"} {
		if i := bytes.Index(src, []byte(q+name+q)); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	if best < 0 {
		return loc, false
	}
	loc.Line = bytes.Count(src[:best], []byte("\n"))
	loc.Col = best + 1 - (bytes.LastIndexByte(src[:best], '\n') + 1)
	return loc, true
}

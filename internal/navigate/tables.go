package navigate

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// TableRefSource says how a table was referred to.
type TableRefSource int

const (
	// SourceRef is a ref() or resolve() call, or dbt's ref() or source().
	SourceRef TableRefSource = iota
	// SourceRaw is a literal project.dataset.table in SQL.
	SourceRaw
	// SourceSelf is self(), or dbt's this: the file's own table.
	SourceSelf
)

// TableRef is a table referred to at some place in a file.
type TableRef struct {
	Source TableRefSource
	Target project.Target
	// Action is the project's action for the table: nil for a raw id that
	// isn't one of the project's.
	Action *project.Action
	// Start and End bound the reference's text.
	Start, End int
}

// selfCall matches self().
var selfCall = regexp.MustCompile(`\bself\s*\(\s*\)`)

// TableRefsAt returns the table referred to at offset: the file's own table
// for self(), every action a ref() or resolve() matches, or a literal
// project.dataset.table outside comments and JavaScript; in a dbt project,
// the file's own table for {{ this }} and what a ref() or source() matches.
// It returns nil when the cursor is on none of these. Without a graph only
// literal ids work.
func TableRefsAt(p Project, file string, src []byte, offset int) []TableRef {
	if offset < 0 || offset > len(src) {
		return nil
	}
	if p.Dbt {
		return dbtTableRefsAt(p, file, src, offset)
	}
	for _, m := range selfCall.FindAllIndex(src, -1) {
		if offset >= m[0] && offset <= m[1] {
			return p.selfRef(file, m[0], m[1])
		}
	}

	if args, start, end, ok := refArgsAt(src, offset); ok {
		want, valid := parseRefArgs(args)
		if !valid {
			return nil
		}
		var out []TableRef
		for _, a := range refMatches(p, want) {
			out = append(out, TableRef{Source: SourceRef, Target: a.Target, Action: a, Start: start, End: end})
		}
		return out
	}

	inComment := commentChecker(src)
	for _, m := range bqTable.FindAllIndex(src, -1) {
		if offset < m[0] || offset > m[1] {
			continue
		}
		if isJSFile(file) || inComment(m[0]) || inSpans(jsSpans(src), m[0]) {
			return nil
		}
		return []TableRef{p.rawTableRef(src, m[0], m[1])}
	}
	return nil
}

// selfRef is the reference to file's own table at start:end: self(), or
// dbt's {{ this }}.
func (p Project) selfRef(file string, start, end int) []TableRef {
	if p.Graph == nil {
		return nil
	}
	rel, err := filepath.Rel(p.Root, file)
	if err != nil {
		return nil
	}
	if actions := p.Graph.ActionsInFile(filepath.ToSlash(rel)); len(actions) > 0 {
		return []TableRef{{Source: SourceSelf, Target: actions[0].Target, Action: actions[0], Start: start, End: end}}
	}
	return nil
}

// rawTableRef is the literal project.dataset.table at src[start:end].
func (p Project) rawTableRef(src []byte, start, end int) TableRef {
	parts := strings.Split(string(src[start:end]), ".")
	t := project.Target{Database: parts[0], Schema: parts[1], Name: parts[2]}
	ref := TableRef{Source: SourceRaw, Target: t, Start: start, End: end}
	if p.Graph != nil {
		ref.Action = p.Graph.Actions[t.ID()]
	}
	return ref
}

// thisWord matches dbt's this, in a {{ }} or on its own.
var thisWord = regexp.MustCompile(`\bthis\b`)

// TableRefs returns every table src refers to, in file order: what
// TableRefsAt finds at each ref(), resolve() and self() call and literal
// project.dataset.table; in a dbt project at each ref(), source() and
// {{ this }}. A ref() that matches several actions gives one TableRef for
// each.
func TableRefs(p Project, file string, src []byte) []TableRef {
	var syms []Symbol
	var selves [][]int
	if p.Dbt {
		syms = DbtDocumentSymbols(file, src)
		selves = thisWord.FindAllIndex(src, -1)
	} else {
		syms = DocumentSymbols(file, src)
		selves = selfCall.FindAllIndex(src, -1)
	}
	type key struct {
		start int
		id    project.ActionID
	}
	seen := map[key]bool{}
	var out []TableRef
	add := func(offset int) {
		for _, r := range TableRefsAt(p, file, src, offset) {
			k := key{r.Start, r.Target.ID()}
			if !seen[k] {
				seen[k] = true
				out = append(out, r)
			}
		}
	}
	for _, s := range ReferencesFirst(syms) {
		if s.Kind == SymbolRef || s.Kind == SymbolTable {
			add((s.Start + s.End) / 2)
		}
	}
	for _, m := range selves {
		add(m[0])
	}
	slices.SortStableFunc(out, func(a, b TableRef) int { return a.Start - b.Start })
	return out
}

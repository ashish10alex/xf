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
	return p.tableRefsAt(file, src, offset, newRefScan(src))
}

// tableRefsAt is TableRefsAt with src's scan shared between offsets.
func (p Project) tableRefsAt(file string, src []byte, offset int, sc *refScan) []TableRef {
	if offset < 0 || offset > len(src) {
		return nil
	}
	if p.Dbt {
		return p.dbtTableRefsIn(file, src, offset, sc)
	}
	for _, m := range sc.selfCalls() {
		if offset >= m[0] && offset <= m[1] {
			return p.selfRef(file, m[0], m[1])
		}
	}

	if args, start, end, ok := refArgsIn(src, sc.refCalls(), offset); ok {
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

	inComment := sc.inComment()
	for _, m := range sc.bqTables() {
		if offset < m[0] || offset > m[1] {
			continue
		}
		if isJSFile(file) || inComment(m[0]) || inSpans(sc.jsSpans(), m[0]) {
			return nil
		}
		return []TableRef{p.rawTableRef(src, m[0], m[1])}
	}
	return nil
}

// refScan holds what tableRefsAt finds in a whole file, each found the first
// time it is needed, so that TableRefs scans the file once and not once per
// reference.
type refScan struct {
	src []byte

	selves, refs, tables [][]int
	spans                []span
	tags                 []jinjaTag
	comment              func(offset int) bool

	doneSelves, doneRefs, doneTables, doneSpans, doneTags bool
}

func newRefScan(src []byte) *refScan { return &refScan{src: src} }

// selfCalls are src's self() calls.
func (sc *refScan) selfCalls() [][]int {
	if !sc.doneSelves {
		sc.selves, sc.doneSelves = selfCall.FindAllIndex(sc.src, -1), true
	}
	return sc.selves
}

// refCalls are src's ref() and resolve() calls, with their arguments.
func (sc *refScan) refCalls() [][]int {
	if !sc.doneRefs {
		sc.refs, sc.doneRefs = refCall.FindAllSubmatchIndex(sc.src, -1), true
	}
	return sc.refs
}

// bqTables are src's literal project.dataset.table ids.
func (sc *refScan) bqTables() [][]int {
	if !sc.doneTables {
		sc.tables, sc.doneTables = bqTable.FindAllIndex(sc.src, -1), true
	}
	return sc.tables
}

// jsSpans are src's JavaScript parts.
func (sc *refScan) jsSpans() []span {
	if !sc.doneSpans {
		sc.spans, sc.doneSpans = jsSpans(sc.src), true
	}
	return sc.spans
}

// jinjaTags are src's Jinja tags.
func (sc *refScan) jinjaTags() []jinjaTag {
	if !sc.doneTags {
		sc.tags, sc.doneTags = jinjaTags(sc.src), true
	}
	return sc.tags
}

// inComment is src's commentChecker.
func (sc *refScan) inComment() func(offset int) bool {
	if sc.comment == nil {
		sc.comment = commentChecker(sc.src)
	}
	return sc.comment
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
	sc := newRefScan(src)
	add := func(offset int) {
		for _, r := range p.tableRefsAt(file, src, offset, sc) {
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

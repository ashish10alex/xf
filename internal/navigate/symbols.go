package navigate

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
)

// SymbolKind says what a Symbol is.
type SymbolKind int

const (
	SymbolCTE SymbolKind = iota
	SymbolRef
	// SymbolTable is a literal project.dataset.table reference in SQL.
	SymbolTable
	// SymbolMacro is a dbt macro or generic test definition.
	SymbolMacro
)

// Detail is the short label editors show next to the symbol's name.
func (k SymbolKind) Detail() string {
	return [...]string{"cte", "ref", "bq_table", "macro"}[k]
}

// Symbol is one entry of a sqlx file's outline. Offsets are bytes into the
// file.
type Symbol struct {
	Name string
	Kind SymbolKind
	// Start and End bound the whole symbol: a CTE's name through its body,
	// a ref() call, a table name.
	Start, End int
	// SelStart and SelEnd bound what the cursor should land on: a CTE's
	// name, a ref's first argument.
	SelStart, SelEnd int
	// Children are the refs and tables inside a CTE's body.
	Children []Symbol
}

// bqTable matches project.dataset.table.
var bqTable = regexp.MustCompile(`[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`)

// blockComment matches /* ... */.
var blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// DocumentSymbols outlines a sqlx file the way vscode-dataform-tools does for
// Go to Symbol: its CTEs, with the ref() calls and literal BigQuery tables
// each one reads nested under it; references outside any CTE are top level.
// JavaScript files have no outline here.
func DocumentSymbols(file string, src []byte) []Symbol {
	if isJSFile(file) {
		return nil
	}
	inComment := commentChecker(src)

	var refs []Symbol
	for _, m := range refCall.FindAllSubmatchIndex(src, -1) {
		if inComment(m[0]) {
			continue
		}
		s := Symbol{Name: compactCall(src[m[0]:m[1]]), Kind: SymbolRef, Start: m[0], End: m[1], SelStart: m[0], SelEnd: m[1]}
		// Land on the first argument, so go-to-definition works from there.
		if lit := stringLit.FindIndex(src[m[2]:m[3]]); lit != nil {
			s.SelStart, s.SelEnd = m[2]+lit[0]+1, m[2]+lit[1]-1
		}
		refs = append(refs, s)
	}
	spans := jsSpans(src)
	for _, m := range bqTable.FindAllIndex(src, -1) {
		if inComment(m[0]) || inSpans(spans, m[0]) {
			continue
		}
		refs = append(refs, Symbol{Name: string(src[m[0]:m[1]]), Kind: SymbolTable, Start: m[0], End: m[1], SelStart: m[0], SelEnd: m[1]})
	}
	return nestUnderCTEs(findCTEs(src), refs)
}

// commentChecker returns a function reporting whether an offset in src is
// inside a comment: a /* */ block, or after -- or // on its line. Like the
// extension, it doesn't mind that -- might be inside a string.
func commentChecker(src []byte) func(offset int) bool {
	comments := blockComment.FindAllIndex(src, -1)
	return func(offset int) bool {
		for _, c := range comments {
			if offset >= c[0] && offset < c[1] {
				return true
			}
		}
		before := src[bytes.LastIndexByte(src[:offset], '\n')+1 : offset]
		return bytes.Contains(before, []byte("--")) || bytes.Contains(before, []byte("//"))
	}
}

// nestUnderCTEs puts each reference under the innermost CTE whose body holds
// it and returns the top-level symbols in file order.
func nestUnderCTEs(ctes []cte, refs []Symbol) []Symbol {
	top := make([]Symbol, len(ctes))
	for i, c := range ctes {
		top[i] = Symbol{Name: c.Name, Kind: SymbolCTE, Start: c.Start, End: c.End, SelStart: c.NameStart, SelEnd: c.NameEnd}
	}
	slices.SortFunc(refs, func(a, b Symbol) int { return a.Start - b.Start })
	var loose []Symbol
	for _, r := range refs {
		in := -1
		for i, c := range ctes {
			if r.Start >= c.Start && r.Start < c.End && (in < 0 || c.End-c.Start < ctes[in].End-ctes[in].Start) {
				in = i
			}
		}
		if in >= 0 {
			top[in].Children = append(top[in].Children, r)
		} else {
			loose = append(loose, r)
		}
	}
	top = append(top, loose...)
	slices.SortStableFunc(top, func(a, b Symbol) int { return a.Start - b.Start })
	return top
}

func inSpans(spans []span, offset int) bool {
	for _, s := range spans {
		if offset >= s.start && offset < s.end {
			return true
		}
	}
	return false
}

// compactCall renders a possibly multi-line call on one line:
// ref(\n "a",\n "b"\n) becomes ref("a", "b").
func compactCall(call []byte) string {
	s := strings.Join(strings.Fields(string(call)), " ")
	for _, r := range [][2]string{{"( ", "("}, {" )", ")"}, {"{ ", "{"}, {" }", "}"}} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return s
}

// ReferencesFirst flattens an outline into the order a symbol picker shows
// best: every ref and table first, then the CTEs, each group in file order.
// The CTEs lose their children, which already appear in the first group.
func ReferencesFirst(symbols []Symbol) []Symbol {
	var refs, ctes []Symbol
	for _, s := range symbols {
		if s.Kind != SymbolCTE {
			refs = append(refs, s)
			continue
		}
		refs = append(refs, s.Children...)
		s.Children = nil
		ctes = append(ctes, s)
	}
	slices.SortStableFunc(refs, func(a, b Symbol) int { return a.Start - b.Start })
	return append(refs, ctes...)
}

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

// dbtRef is what a ref() or source() call asks for.
type dbtRef struct {
	// Source is set for a source() call.
	Source string
	// Package is ref('package', 'name')'s; empty for any.
	Package, Name string
	// Version is ref('name', v=2)'s; Versioned says v= was given.
	Version   string
	Versioned bool
}

// parseDbtCall understands ref('name'), ref('package', 'name'),
// ref('name', v=2) (or version=2) and source('source', 'name'), with
// literal arguments.
func parseDbtCall(c jinjaCall) (dbtRef, bool) {
	pos := c.positional()
	for _, a := range pos {
		if !a.Literal || a.Value == "" {
			return dbtRef{}, false
		}
	}
	var r dbtRef
	switch c.Func {
	case "ref":
		switch len(pos) {
		case 1:
			r.Name = pos[0].Value
		case 2:
			r.Package, r.Name = pos[0].Value, pos[1].Value
		default:
			return dbtRef{}, false
		}
		if v, ok := c.keyword("v", "version"); ok {
			if !v.Literal {
				return dbtRef{}, false
			}
			r.Version, r.Versioned = v.Value, true
		}
	case "source":
		if len(pos) != 2 {
			return dbtRef{}, false
		}
		r.Source, r.Name = pos[0].Value, pos[1].Value
	default:
		return dbtRef{}, false
	}
	return r, true
}

// dbtMatches returns the actions a ref() or source() in file asks for,
// ordered by file then ID. A ref() without a package, matched in several,
// keeps those of file's own package, else the project's, as dbt prefers;
// disabled resources count only when nothing enabled matches.
func dbtMatches(p Project, r dbtRef, file string) []*project.Action {
	if p.Graph == nil {
		return nil
	}
	var enabled, disabled []*project.Action
	for _, a := range p.Graph.Actions {
		n := a.DbtName
		if n == nil || n.Name != r.Name || n.Source != r.Source {
			continue
		}
		if r.Source == "" {
			if r.Package != "" && n.Package != r.Package || r.Versioned && n.Version != r.Version || !r.Versioned && !n.Latest {
				continue
			}
		}
		if a.Disabled {
			disabled = append(disabled, a)
		} else {
			enabled = append(enabled, a)
		}
	}
	matches := enabled
	if len(matches) == 0 {
		matches = disabled
	}
	if r.Source == "" && r.Package == "" {
		for _, pkg := range []string{p.dbtPackageOf(file), p.Graph.Package} {
			in := slices.DeleteFunc(slices.Clone(matches), func(a *project.Action) bool { return a.DbtName.Package != pkg })
			if len(in) > 0 && len(in) < len(matches) {
				matches = in
				break
			}
		}
	}
	slices.SortFunc(matches, func(x, y *project.Action) int {
		return cmp.Or(cmp.Compare(x.FileName, y.FileName), cmp.Compare(x.ID, y.ID))
	})
	return matches
}

// dbtPackageOf is the package file belongs to: an installed one's under
// dbt_packages/, else the project's.
func (p Project) dbtPackageOf(file string) string {
	if rel, err := filepath.Rel(p.Root, file); err == nil {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) > 2 && parts[0] == "dbt_packages" {
			return parts[1]
		}
	}
	if p.Graph == nil {
		return ""
	}
	return p.Graph.Package
}

// macrosCalled returns the macros a call to fn in file may run: fn names a
// package's macro as package.name, and the project's (or file's own
// package's) by name alone.
func (p Project) macrosCalled(fn, file string) []project.Macro {
	if p.Graph == nil {
		return nil
	}
	pkg, name := "", fn
	if i := strings.LastIndexByte(fn, '.'); i >= 0 {
		pkg, name = fn[:i], fn[i+1:]
	}
	var out []project.Macro
	for _, m := range p.Graph.Macros {
		if m.Name == name && (pkg == "" || m.Package == pkg) {
			out = append(out, m)
		}
	}
	if pkg == "" && len(out) > 1 {
		for _, own := range []string{p.dbtPackageOf(file), p.Graph.Package} {
			if i := slices.IndexFunc(out, func(m project.Macro) bool { return m.Package == own }); i >= 0 {
				return out[i : i+1]
			}
		}
	}
	return out
}

// dbtDefinition resolves the ref(), source() or macro call around offset.
// ok is false when the cursor is on none, so CTEs can have a go.
func dbtDefinition(p Project, file string, src []byte, offset int) (locs []Location, ok bool) {
	c, found := callAt(src, offset)
	if !found {
		return nil, false
	}
	if c.Func == "ref" || c.Func == "source" {
		r, valid := parseDbtCall(c)
		if !valid {
			return nil, true
		}
		for _, a := range dbtMatches(p, r, file) {
			loc := p.actionLocation(a)
			if a.Kind == project.KindSource {
				loc = p.sourceLocation(a)
			}
			if !slices.Contains(locs, loc) {
				locs = append(locs, loc)
			}
		}
		return locs, true
	}
	for _, m := range p.macrosCalled(c.Func, file) {
		locs = append(locs, p.macroLocation(m))
	}
	return locs, len(locs) > 0
}

// yamlName matches a `name:` key, in a list item or not, and its value.
var yamlName = regexp.MustCompile(`^(\s*(?:-\s+)?)name:\s*["']?([^"'#\s]+)["']?\s*(?:#.*)?$`)

// sourceLocation is where a source table is declared: its `- name:` line
// under its source's in the YAML file, else the file's top.
func (p Project) sourceLocation(a *project.Action) Location {
	loc, _ := p.sourceAt(a)
	return loc
}

// sourceAt is sourceLocation, ok false when the table isn't listed under
// its source.
func (p Project) sourceAt(a *project.Action) (Location, bool) {
	path := filepath.Join(p.Root, filepath.FromSlash(a.FileName))
	loc := Location{File: path}
	src, err := p.read(path)
	if err != nil || a.DbtName == nil {
		return loc, false
	}
	lines := bytes.Split(src, []byte("\n"))
	sourceCol := -1
	for i, line := range lines {
		m := yamlName.FindSubmatchIndex(line)
		if m == nil {
			continue
		}
		col, value := m[3], string(line[m[4]:m[5]])
		switch {
		case sourceCol < 0:
			if value == a.DbtName.Source {
				sourceCol = col
				loc.Line, loc.Col = i, m[4]
			}
		case col <= sourceCol:
			return loc, false // the next source: the table isn't listed
		case value == a.DbtName.Name:
			loc.Line, loc.Col = i, m[4]
			return loc, true
		}
	}
	return loc, false
}

// macroLocation is where a macro is defined: its name in the {% macro %}
// (or {% test %}) tag, else the top of its file.
func (p Project) macroLocation(m project.Macro) Location {
	path := filepath.Join(p.Root, filepath.FromSlash(m.FileName))
	loc := Location{File: path}
	src, err := p.read(path)
	if err != nil {
		return loc
	}
	if b, ok := findMacroBlock(src, m.Name); ok {
		return locationAt(path, src, b.NameStart)
	}
	return loc
}

// findMacroBlock finds the definition of the macro dbt calls name: a
// generic test's is test_ and its name.
func findMacroBlock(src []byte, name string) (macroBlock, bool) {
	for _, b := range macroBlocks(src) {
		if b.Keyword == "macro" && b.Name == name || b.Keyword == "test" && "test_"+b.Name == name {
			return b, true
		}
	}
	return macroBlock{}, false
}

// MacroRef is a macro called at some place in a file.
type MacroRef struct {
	Macro project.Macro
	// Signature is the macro's definition as a call, name(args), as its
	// file has it; its name alone when the file can't be read.
	Signature string
	// Start and End bound the call.
	Start, End int
}

// MacrosAt returns the macros the call at offset in a dbt project's file
// may run, or nil when the cursor is on no macro call.
func MacrosAt(p Project, file string, src []byte, offset int) []MacroRef {
	if !p.Dbt || offset < 0 || offset > len(src) {
		return nil
	}
	c, ok := callAt(src, offset)
	if !ok || c.Func == "ref" || c.Func == "source" {
		return nil
	}
	var out []MacroRef
	for _, m := range p.macrosCalled(c.Func, file) {
		ref := MacroRef{Macro: m, Signature: m.Name + "()", Start: c.Start, End: c.End}
		if text, err := p.read(filepath.Join(p.Root, filepath.FromSlash(m.FileName))); err == nil {
			if b, ok := findMacroBlock(text, m.Name); ok {
				ref.Signature = b.Signature
			}
		}
		out = append(out, ref)
	}
	return out
}

// dbtTableRefsAt is TableRefsAt for a dbt project: {{ this }}, a ref() or
// source() call, or a literal table outside tags and comments.
func dbtTableRefsAt(p Project, file string, src []byte, offset int) []TableRef {
	tags := jinjaTags(src)
	for _, t := range tags {
		if offset < t.Start || offset > t.End {
			continue
		}
		for _, tok := range jinjaTokens(src, t.Start, t.End) {
			if tok.kind == jtokIdent && offset >= tok.start && offset <= tok.end && (string(src[tok.start:tok.end]) == "this" || strings.HasPrefix(string(src[tok.start:tok.end]), "this.")) {
				return p.selfRef(file, tok.start, tok.end)
			}
		}
		c, ok := callAt(src, offset)
		if !ok {
			return nil
		}
		r, valid := parseDbtCall(c)
		if !valid {
			return nil
		}
		var out []TableRef
		for _, a := range dbtMatches(p, r, file) {
			out = append(out, TableRef{Source: SourceRef, Target: a.Target, Action: a, Start: c.Start, End: c.End})
		}
		return out
	}
	inComment := commentChecker(src)
	for _, m := range bqTable.FindAllIndex(src, -1) {
		if offset < m[0] || offset > m[1] {
			continue
		}
		if inComment(m[0]) || inTags(tags, m[0]) {
			return nil
		}
		return []TableRef{p.rawTableRef(src, m[0], m[1])}
	}
	return nil
}

func inTags(tags []jinjaTag, offset int) bool {
	for _, t := range tags {
		if offset >= t.Start && offset < t.End {
			return true
		}
	}
	return false
}

// dbtCompletions is Completions for a dbt project: names inside ref('…')
// and source('…', '…'), macros where a call can go, and whole ref() and
// source() calls at the start of a {{ }}.
func dbtCompletions(p Project, file string, src []byte, offset int) []Completion {
	t, ok := openTag(src, offset)
	if !ok {
		return nil
	}
	calls := jinjaCalls(src, t.Start, offset)
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		if c.Closed || len(c.Args) == 0 {
			continue
		}
		last := c.Args[len(c.Args)-1]
		if !last.Open || last.End != offset || last.Key != "" {
			continue
		}
		return argCompletions(p, c, last)
	}
	toks := jinjaTokens(src, t.Start, offset)
	word := offset
	switch n := len(toks); {
	case n > 0 && toks[n-1].kind == jtokIdent && toks[n-1].end == offset:
		word = toks[n-1].start
		toks = toks[:n-1]
	case n > 1 && src[offset-1] == '.' && toks[n-1].start == offset-1 && toks[n-2].kind == jtokIdent && toks[n-2].end == offset-1:
		// package. with the macro's name still to come
		word = toks[n-2].start
		toks = toks[:n-2]
	case n > 0 && toks[n-1].kind == jtokString && toks[n-1].open:
		return nil
	}
	atStart := len(toks) == 0 || len(toks) == 1 && toks[0].kind == jtokOther && src[toks[0].start] == '-'
	if !t.Expr && atStart {
		return nil // a statement's keyword: if, set, for
	}
	var out []Completion
	if t.Expr && atStart {
		out = append(out, callCompletions(p, src, word, offset)...)
	}
	return append(out, macroCompletions(p, file, word)...)
}

// argCompletions completes the string argument arg of a ref() or source()
// call being typed.
func argCompletions(p Project, c jinjaCall, arg jinjaArg) []Completion {
	pos := c.positional()
	n := len(pos) - 1
	var out []Completion
	add := func(label, detail string) {
		out = append(out, Completion{Kind: CompletionRef, Label: label, Detail: detail, InsertText: label, FilterText: label, Start: arg.Start})
	}
	switch {
	case c.Func == "ref" && n == 0:
		for _, a := range refable(p.Graph) {
			add(a.DbtName.Name, dbtDetail(p, a))
		}
	case c.Func == "ref" && n == 1 && pos[0].Literal:
		for _, a := range refable(p.Graph) {
			if a.DbtName.Package == pos[0].Value {
				add(a.DbtName.Name, dbtDetail(p, a))
			}
		}
	case c.Func == "source" && n == 0:
		seen := map[string]bool{}
		for _, a := range sources(p.Graph) {
			if !seen[a.DbtName.Source] {
				seen[a.DbtName.Source] = true
				add(a.DbtName.Source, "source")
			}
		}
	case c.Func == "source" && n == 1 && pos[0].Literal:
		for _, a := range sources(p.Graph) {
			if a.DbtName.Source == pos[0].Value {
				add(a.DbtName.Name, dbtDetail(p, a))
			}
		}
	}
	return out
}

// callCompletions are whole ref() and source() calls for the start of a
// {{ }} tag, closing it unless it already is.
func callCompletions(p Project, src []byte, start, offset int) []Completion {
	prefix, suffix := "", " }}"
	if start > 0 && src[start-1] == '{' {
		prefix = " "
	}
	rest := offset
	for rest < len(src) && isWordByte(src[rest]) {
		rest++
	}
	if after := bytes.TrimLeft(src[rest:], " \t"); bytes.HasPrefix(after, []byte("}}")) || bytes.HasPrefix(after, []byte("-}}")) {
		suffix = ""
	}
	var out []Completion
	for _, a := range refable(p.Graph) {
		n := a.DbtName
		call := "ref('" + n.Name + "')"
		out = append(out, Completion{Kind: CompletionRef, Label: call, Detail: dbtDetail(p, a), InsertText: prefix + call + suffix, FilterText: n.Name, Start: start})
	}
	for _, a := range sources(p.Graph) {
		n := a.DbtName
		call := "source('" + n.Source + "', '" + n.Name + "')"
		out = append(out, Completion{Kind: CompletionRef, Label: call, Detail: dbtDetail(p, a), InsertText: prefix + call + suffix, FilterText: n.Name, Start: start})
	}
	return out
}

// macroCompletions are the macros a call in file can name: the project's
// (and file's own package's) by name, the rest as package.name.
func macroCompletions(p Project, file string, start int) []Completion {
	if p.Graph == nil {
		return nil
	}
	own := map[string]bool{p.Graph.Package: true, p.dbtPackageOf(file): true}
	var out []Completion
	for _, m := range p.Graph.Macros {
		if strings.HasPrefix(m.Name, "test_") || strings.HasPrefix(m.Name, "materialization_") {
			continue // run by dbt, never called
		}
		name := m.Name
		if !own[m.Package] {
			name = m.Package + "." + m.Name
		}
		out = append(out, Completion{Kind: CompletionMacro, Label: name + "()", Detail: "macro · " + m.Package, InsertText: name + "(", FilterText: name, Start: start})
	}
	return out
}

// refable are the enabled resources ref() can name, one per name and
// package (a versioned model's latest), sorted by name.
func refable(g *project.Graph) []*project.Action {
	var out []*project.Action
	if g == nil {
		return nil
	}
	for _, a := range g.Actions {
		if n := a.DbtName; n != nil && n.Source == "" && n.Latest && !a.Disabled {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(x, y *project.Action) int {
		return cmp.Or(cmp.Compare(x.DbtName.Name, y.DbtName.Name), cmp.Compare(x.DbtName.Package, y.DbtName.Package))
	})
	return out
}

// sources are the enabled sources, sorted by source then name.
func sources(g *project.Graph) []*project.Action {
	var out []*project.Action
	if g == nil {
		return nil
	}
	for _, a := range g.Actions {
		if n := a.DbtName; n != nil && n.Source != "" && !a.Disabled {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(x, y *project.Action) int {
		return cmp.Or(cmp.Compare(x.DbtName.Source, y.DbtName.Source), cmp.Compare(x.DbtName.Name, y.DbtName.Name))
	})
	return out
}

// dbtDetail is a completion's detail: the schema and kind, and the package
// when it isn't the project's.
func dbtDetail(p Project, a *project.Action) string {
	d := actionDetail(a)
	if pkg := a.DbtName.Package; pkg != "" && pkg != p.Graph.Package {
		d += " · " + pkg
	}
	return d
}

// DbtDocumentSymbols outlines a dbt project's SQL file: its CTEs with the
// ref() and source() calls and literal tables each reads nested under it,
// and its macro and test definitions.
func DbtDocumentSymbols(file string, src []byte) []Symbol {
	tags := jinjaTags(src)
	var refs []Symbol
	for _, t := range tags {
		for _, c := range jinjaCalls(src, t.Start, t.End) {
			if _, ok := parseDbtCall(c); !ok || !c.Closed {
				continue
			}
			first := c.positional()[0]
			refs = append(refs, Symbol{Name: compactCall(src[c.Start:c.End]), Kind: SymbolRef, Start: c.Start, End: c.End, SelStart: first.Start, SelEnd: first.End})
		}
	}
	inComment := commentChecker(src)
	for _, m := range bqTable.FindAllIndex(src, -1) {
		if inComment(m[0]) || inTags(tags, m[0]) {
			continue
		}
		refs = append(refs, Symbol{Name: string(src[m[0]:m[1]]), Kind: SymbolTable, Start: m[0], End: m[1], SelStart: m[0], SelEnd: m[1]})
	}
	out := nestUnderCTEs(findCTEs(src), refs)
	for _, b := range macroBlocks(src) {
		out = append(out, Symbol{Name: b.Signature, Kind: SymbolMacro, Start: b.Start, End: b.End, SelStart: b.NameStart, SelEnd: b.NameEnd})
	}
	slices.SortStableFunc(out, func(a, b Symbol) int { return a.Start - b.Start })
	return out
}

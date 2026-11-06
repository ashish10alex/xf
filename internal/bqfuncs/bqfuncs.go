// Package bqfuncs is what xf knows of BigQuery's functions, for the
// completion, hover and signature help of xf web's Scratch editor.
//
// Two files are vscode-dataform-tools' own, copied unchanged by
// `just bigquery-functions`: bigquery-functions.json is its catalog, each
// function's typed arguments, the values they take, what it returns, an
// example and its docs; bigquery.code-snippets.json the snippets it borrowed,
// which still describe the functions the catalog has not taken in yet.
// clauses.json and overrides.json are xf's own, in the catalog's format:
// the first has PIVOT and UNPIVOT, written as a call after a table, so they
// get the same snippet, card and signature help; the second shorter cards
// that take the place of the catalog's, as ARRAY_AGG's.
// Snippets here are CodeMirror's: `${1:name}` is a field, and there is no
// dropdown, so a field of fixed values holds the first and the editor offers
// the rest.
package bqfuncs

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf16"
)

//go:embed bigquery-functions.json
var catalogJSON []byte

//go:embed bigquery.code-snippets.json
var snippetsJSON []byte

//go:embed clauses.json
var clausesJSON []byte

//go:embed overrides.json
var overridesJSON []byte

// Func is a function as the editor shows it.
type Func struct {
	Name string `json:"name"`
	// Snippet is what completing it inserts, as a CodeMirror snippet.
	Snippet string `json:"snippet"`
	// Detail is its first signature on a line, beside its completion.
	Detail string `json:"detail"`
	Doc    string `json:"doc,omitempty"`
	// Signatures, Notes and Example are the catalog's; a borrowed snippet's
	// function has none.
	Signatures []Signature `json:"signatures,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	Example    *Example    `json:"example,omitempty"`
	// Body is a borrowed snippet as plain SQL, with every optional part it
	// writes in brackets, where the snippet has only what is required.
	Body string `json:"body,omitempty"`
	URL  string `json:"url"`
	// Searched says URL searches the docs for the name: the snippet had no
	// link of its own.
	Searched bool `json:"searched,omitempty"`
	// Clause says it is an operator after a table in FROM, as PIVOT: it is
	// offered there, not where a function goes.
	Clause bool `json:"clause,omitempty"`
}

// Signature is one way to call a function.
type Signature struct {
	// Label is the signature on a line, as `DATE_TRUNC(date_value: DATE,
	// granularity: date part) → DATE`.
	Label   string  `json:"label"`
	Params  []Param `json:"params"`
	Returns string  `json:"returns"`
}

// Param is an argument of a signature.
type Param struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Optional bool   `json:"optional,omitempty"`
	Repeated bool   `json:"repeated,omitempty"`
	// Values is the fixed set it takes, offered as completions.
	Values []string `json:"values,omitempty"`
	// Before is the keyword in front of it where that is no comma, as
	// CAST's AS.
	Before string `json:"before,omitempty"`
	// Span is where its name and type are in the label, in UTF-16 units as
	// JavaScript counts them.
	Span [2]int `json:"span"`
	// Text is what is said of it: its doc, its values, whether optional.
	Text string `json:"text,omitempty"`
}

// Example is a call and what it returns.
type Example struct {
	SQL    string `json:"sql"`
	Result string `json:"result"`
}

// All is every function, in name order: the catalog's, the borrowed
// snippets' it has not taken in, and the clauses.
var All = sync.OnceValues(load)

func load() ([]Func, error) {
	var sets struct {
		ValueSets map[string][]string `json:"valueSets"`
	}
	if err := json.Unmarshal(catalogJSON, &sets); err != nil {
		return nil, fmt.Errorf("bigquery-functions.json: %w", err)
	}
	cat, err := loadCatalog(catalogJSON, nil)
	if err != nil {
		return nil, fmt.Errorf("bigquery-functions.json: %w", err)
	}
	// xf's files name the catalog's value sets, as date_granularity.
	overrides, err := loadCatalog(overridesJSON, sets.ValueSets)
	if err != nil {
		return nil, fmt.Errorf("overrides.json: %w", err)
	}
	clauses, err := loadCatalog(clausesJSON, sets.ValueSets)
	if err != nil {
		return nil, fmt.Errorf("clauses.json: %w", err)
	}
	have := map[string]bool{}
	for _, f := range cat {
		have[f.Name] = true
	}
	for i := range clauses {
		clauses[i].Clause = true
		have[clauses[i].Name] = true
	}
	cat = append(cat, clauses...)
	borrowed, err := loadBorrowed(snippetsJSON, have)
	if err != nil {
		return nil, fmt.Errorf("bigquery.code-snippets.json: %w", err)
	}
	out := append(cat, borrowed...)
	for _, o := range overrides {
		i := slices.IndexFunc(out, func(f Func) bool { return f.Name == o.Name })
		if i < 0 {
			return nil, fmt.Errorf("overrides.json: there is no %s to take the place of", o.Name)
		}
		out[i] = o
	}
	slices.SortStableFunc(out, func(a, b Func) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// ---- the catalog ----

type catalogFile struct {
	ValueSets map[string][]string `json:"valueSets"`
	Functions []struct {
		Name       string `json:"name"`
		Doc        string `json:"doc"`
		Signatures []struct {
			Params  []catalogParam `json:"params"`
			Returns string         `json:"returns"`
		} `json:"signatures"`
		Window  string   `json:"window"`
		Over    string   `json:"over"`
		Notes   []string `json:"notes"`
		Example *Example `json:"example"`
		URL     string   `json:"url"`
	} `json:"functions"`
}

type catalogParam struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Doc      string          `json:"doc"`
	Optional bool            `json:"optional"`
	Repeated bool            `json:"repeated"`
	Values   json.RawMessage `json:"values"` // a list, or the name of a value set
	Before   string          `json:"before"`
	Snippet  string          `json:"snippet"`
}

// field is a `{text}` of a catalog snippet: a field holding text, or a
// dropdown of `A|B`.
var field = regexp.MustCompile(`\{([^{}]*)\}`)

// loadCatalog reads a file in the catalog's format; its params' values may
// name its own value sets or shared's.
func loadCatalog(data []byte, shared map[string][]string) ([]Func, error) {
	var file catalogFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	for name, set := range shared {
		if _, ok := file.ValueSets[name]; !ok {
			if file.ValueSets == nil {
				file.ValueSets = map[string][]string{}
			}
			file.ValueSets[name] = set
		}
	}
	values := func(p catalogParam) ([]string, error) {
		if len(p.Values) == 0 {
			// A snippet of one dropdown is a fixed set too, as CAST's type.
			if m := field.FindStringSubmatch(p.Snippet); m != nil && m[0] == p.Snippet && strings.Contains(m[1], "|") {
				return strings.Split(m[1], "|"), nil
			}
			return nil, nil
		}
		var list []string
		if json.Unmarshal(p.Values, &list) == nil {
			return list, nil
		}
		var name string
		if err := json.Unmarshal(p.Values, &name); err != nil {
			return nil, fmt.Errorf("%s: values is neither a list nor a name", p.Name)
		}
		set, ok := file.ValueSets[name]
		if !ok {
			return nil, fmt.Errorf("%s: no value set named %q", p.Name, name)
		}
		return set, nil
	}

	var out []Func
	for _, cf := range file.Functions {
		f := Func{Name: cf.Name, Doc: cf.Doc, Notes: cf.Notes, Example: cf.Example, URL: cf.URL}
		for _, cs := range cf.Signatures {
			params := []Param{} // [] in JSON for ROW_NUMBER(), not null
			for _, cp := range cs.Params {
				vs, err := values(cp)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", cf.Name, err)
				}
				params = append(params, Param{Name: cp.Name, Type: cp.Type, Optional: cp.Optional, Repeated: cp.Repeated, Values: vs, Before: cp.Before, Text: paramText(cp.Doc, vs, cp.Optional && cp.Type != "")})
			}
			f.Signatures = append(f.Signatures, label(cf.Name, cf.Window, params, cs.Returns))
		}
		if len(f.Signatures) == 0 {
			return nil, fmt.Errorf("%s: no signatures", cf.Name)
		}
		// A required OVER is in the signature already.
		if cf.Window == "optional" {
			f.Notes = append(f.Notes, "Also takes `OVER (...)`.")
		}
		f.Detail = f.Signatures[0].Label
		f.Snippet = catalogSnippet(cf.Name, cf.Window, cf.Over, cf.Signatures[0].Params, f.Signatures[0].Params)
		out = append(out, f)
	}
	return out, nil
}

// label is a signature with its label written, as the extension writes it:
// optional arguments in brackets, with the comma or keyword before them. A
// param with no type is its name alone, and no return type no arrow, as a
// clause's are.
func label(name, window string, params []Param, returns string) Signature {
	var b strings.Builder
	b.WriteString(name + "(")
	u16 := func() int { return len(utf16.Encode([]rune(b.String()))) }
	for i := range params {
		p := &params[i]
		sep := ""
		if i > 0 {
			sep = ", "
			if p.Before != "" {
				sep = " " + p.Before + " "
			}
		}
		switch {
		case !p.Optional:
			b.WriteString(sep)
		case strings.HasPrefix(sep, " "):
			b.WriteString(" [" + strings.TrimLeft(sep, " "))
		default:
			b.WriteString("[" + sep)
		}
		start := u16()
		b.WriteString(p.Name)
		if p.Type != "" {
			b.WriteString(": " + p.Type)
		}
		p.Span = [2]int{start, u16()}
		if p.Repeated {
			b.WriteString(", ...")
		}
		if p.Optional {
			b.WriteString("]")
		}
	}
	b.WriteString(")")
	if window == "required" {
		b.WriteString(" OVER (...)")
	}
	if returns != "" {
		b.WriteString(" → " + returns)
	}
	return Signature{Label: b.String(), Params: params, Returns: returns}
}

// catalogSnippet inserts the first signature's required arguments, and the
// OVER clause a window function needs.
func catalogSnippet(name, window, over string, raw []catalogParam, params []Param) string {
	stop := 0
	fill := func(template string) string {
		return field.ReplaceAllStringFunc(template, func(m string) string {
			text := m[1 : len(m)-1]
			text, _, _ = strings.Cut(text, "|") // no dropdowns: the first, and the rest offered
			stop++
			return fmt.Sprintf("${%d:%s}", stop, text)
		})
	}
	var args strings.Builder
	n := 0
	for i, p := range params {
		if p.Optional {
			continue
		}
		if n > 0 {
			if p.Before != "" {
				args.WriteString(" " + p.Before + " ")
			} else {
				args.WriteString(", ")
			}
		}
		n++
		template := raw[i].Snippet
		if template == "" {
			if len(p.Values) > 0 {
				template = "{" + strings.Join(p.Values, "|") + "}"
			} else {
				template = "{" + p.Name + "}"
			}
		}
		args.WriteString(fill(template))
	}
	body := name + "(" + args.String() + ")"
	if window == "required" {
		if over == "" {
			over = "PARTITION BY {partition_expr} ORDER BY {order_expr}"
		}
		body += " OVER (" + fill(over) + ")"
	}
	return body
}

// maxListedValues is the most values a card lists for a param.
const maxListedValues = 8

// paramText is what is said of a param: its doc as a sentence, its values,
// and whether it is optional. xf's short params, which have no type, leave
// that to the brackets of the signature.
func paramText(doc string, values []string, optional bool) string {
	var parts []string
	if doc != "" {
		parts = append(parts, strings.ToUpper(doc[:1])+doc[1:]+".")
	}
	if len(values) > 0 {
		// A longer list is offered as the argument is typed, and only
		// lengthens the card.
		if len(values) <= maxListedValues {
			parts = append(parts, "One of: "+strings.Join(values, ", ")+".")
		}
	}
	if optional {
		parts = append(parts, "Optional.")
	}
	return strings.Join(parts, " ")
}

// ---- the borrowed snippets ----

type borrowedSnippet struct {
	Body json.RawMessage `json:"body"`
	// Description matches DESCRIPTION too, which some of them use.
	Description json.RawMessage `json:"description"`
}

// funcKey is the key of a function's snippet: its name and a bracket, as
// `LOG(X)`; `IN (value, ...)` is a keyword.
var funcKey = regexp.MustCompile(`^([A-Za-z_][\w.]*)\(`)

var docsLink = regexp.MustCompile(`https?://[^\s)>\]]+`)

func loadBorrowed(data []byte, have map[string]bool) ([]Func, error) {
	var file map[string]map[string]borrowedSnippet
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	snippets := file[".source.sql-bigquery"]
	if snippets == nil {
		return nil, fmt.Errorf("no .source.sql-bigquery snippets")
	}
	keys := make([]string, 0, len(snippets))
	for k := range snippets {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	// One snippet a name: LOG(X) and LOG(X, Y) are both LOG, and LOG() is
	// the one kept, as the extension keeps it.
	chosen := map[string]string{}
	for _, k := range keys {
		m := funcKey.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		name := strings.ToUpper(m[1])
		// PIVOT() and SAFE.prefix() are a clause (clauses.json has it) and a
		// prefix: what their snippet writes is no call of them.
		if have[name] || !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(firstLine(snippets[k].Body))), name+"(") {
			continue
		}
		if _, ok := chosen[name]; !ok || k == name+"()" {
			chosen[name] = k
		}
	}
	var out []Func
	for name, k := range chosen {
		s := snippets[k]
		body, err := joined(s.Body, "\n")
		if err != nil {
			return nil, fmt.Errorf("%s: body: %w", k, err)
		}
		desc, err := joined(s.Description, "\n")
		if err != nil {
			return nil, fmt.Errorf("%s: description: %w", k, err)
		}
		nodes := parseSnippet(body)
		f := Func{Name: name, Snippet: tidy(template(nodes)), Doc: describe(desc)}
		f.Detail = tidy(signature(nodes))
		if plain := strings.TrimSpace(grammar(nodes)); oneLine(plain) != f.Detail {
			f.Body = plain
		}
		if u := docsLink.FindString(desc); u != "" {
			f.URL = u
		} else {
			f.URL = "https://www.google.com/search?q=" + url.QueryEscape("BigQuery "+name+" site:cloud.google.com/bigquery/docs")
			f.Searched = true
		}
		out = append(out, f)
	}
	return out, nil
}

// firstLine is the start of a snippet's body.
func firstLine(raw json.RawMessage) string {
	s, _ := joined(raw, "\n")
	return s
}

// joined is a snippet's string, or its list of lines joined by sep.
func joined(raw json.RawMessage, sep string) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		return "", err
	}
	return strings.Join(lines, sep), nil
}

// describe is a description without its markdown fences.
func describe(desc string) string {
	var keep []string
	for _, line := range strings.Split(desc, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "```") {
			keep = append(keep, strings.TrimRight(line, " "))
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// node is text, or a VS Code tab stop with what it holds.
type node struct {
	text string
	stop int // > 0 for a tab stop; $0 is dropped
	kids []node
}

// parseSnippet reads VS Code's snippet syntax: `$1`, `${1}`, `${1:text}`
// (which may hold others), `${1|a,b|}`, and `\` before `$`, `}` or `\`.
func parseSnippet(s string) []node {
	i := 0
	var seq func(inField bool) []node
	seq = func(inField bool) []node {
		var out []node
		// depth counts the braces a field's text opens, as ML.PREDICT's
		// `{TABLE t | (query)}`: their } is text, not the field's end.
		depth := 0
		var text strings.Builder
		flush := func() {
			if text.Len() > 0 {
				out = append(out, node{text: text.String()})
				text.Reset()
			}
		}
		for i < len(s) {
			c := s[i]
			switch {
			case c == '\\' && i+1 < len(s) && strings.IndexByte(`$}\`, s[i+1]) >= 0:
				if s[i+1] == '}' && depth > 0 { // {ASC|DESC\} closes its brace too
					depth--
				}
				text.WriteByte(s[i+1])
				i += 2
			case c == '{' && inField:
				depth++
				text.WriteByte(c)
				i++
			case c == '}' && inField && depth > 0:
				depth--
				text.WriteByte(c)
				i++
			case c == '}' && inField:
				flush()
				return out
			case c == '$' && i+1 < len(s) && isDigit(s[i+1]):
				flush()
				j := i + 1
				for j < len(s) && isDigit(s[j]) {
					j++
				}
				out = append(out, node{stop: atoi(s[i+1 : j])})
				i = j
			case c == '$' && strings.HasPrefix(s[i:], "${") && i+2 < len(s) && isDigit(s[i+2]):
				flush()
				j := i + 2
				for j < len(s) && isDigit(s[j]) {
					j++
				}
				n := node{stop: atoi(s[i+2 : j])}
				i = j
				switch {
				case i < len(s) && s[i] == ':':
					i++
					n.kids = seq(true)
					i++ // the closing }
				case i < len(s) && s[i] == '|':
					end := strings.Index(s[i:], "|}")
					if end < 0 {
						end = len(s) - i
					}
					first, _, _ := strings.Cut(s[i+1:i+end], ",")
					n.kids = []node{{text: first}}
					i += end + 2
				default:
					i++ // ${1}
				}
				out = append(out, n)
			default:
				text.WriteByte(c)
				i++
			}
		}
		flush()
		return out
	}
	return seq(false)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// render writes nodes, a tab stop as stopFn writes it.
func render(nodes []node, stopFn func(node) string) string {
	var b strings.Builder
	for _, n := range nodes {
		if n.stop == 0 && n.kids == nil {
			b.WriteString(n.text)
			continue
		}
		b.WriteString(stopFn(n))
	}
	return b.String()
}

// grammar is the snippet as plain SQL, each tab stop's text as written.
func grammar(nodes []node) string {
	return render(nodes, func(n node) string { return grammar(n.kids) })
}

// required is a tab stop's text with what is optional left out: `[...]`,
// a choice's alternatives past the first.
func required(n node) string {
	t := grammar(n.kids)
	for {
		next := optional.ReplaceAllString(t, "")
		if next == t {
			break
		}
		t = next
	}
	for {
		next := choice.ReplaceAllStringFunc(t, func(m string) string {
			first, _, _ := strings.Cut(m[1:len(m)-1], "|")
			return first
		})
		if next == t {
			break
		}
		t = next
	}
	t, _, _ = strings.Cut(t, "|")
	t = strings.Join(strings.Fields(t), " ")
	return strings.Trim(t, ", ")
}

var (
	optional = regexp.MustCompile(`\[[^\[\]]*\]`)
	choice   = regexp.MustCompile(`\{[^{}]*\}`)
)

// template is the snippet as CodeMirror's, with only what is required.
func template(nodes []node) string {
	return render(nodes, func(n node) string {
		if n.stop == 0 {
			return required(n)
		}
		t := required(n)
		if t == "" && n.kids != nil {
			return "" // all of it was optional
		}
		return fmt.Sprintf("${%d:%s}", n.stop, t)
	})
}

// signature is the snippet as plain SQL with only what is required.
func signature(nodes []node) string {
	return render(nodes, required)
}

var (
	emptyArg   = regexp.MustCompile(`,\s*\)`)
	openSpace  = regexp.MustCompile(`\(\s+`)
	closeSpace = regexp.MustCompile(`\s+\)`)
)

// tidy is a snippet on one line, with no comma or space left where an
// optional part was taken out.
func tidy(s string) string {
	s = oneLine(s)
	s = emptyArg.ReplaceAllString(s, ")")
	s = openSpace.ReplaceAllString(s, "(")
	return closeSpace.ReplaceAllString(s, ")")
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

package main

// Reading Spark's built-in function pages: mkdocs HTML, one <h3> a
// function, written from each function's ExpressionInfo (its usage, its
// arguments, examples and the version it came in).

import (
	"html"
	"regexp"
	"slices"
	"strings"
)

// Function is one function in the catalog's format (bqfuncs' catalog file).
type Function struct {
	Name       string      `json:"name"`
	Doc        string      `json:"doc,omitempty"`
	Signatures []Signature `json:"signatures"`
	Window     string      `json:"window,omitempty"`
	Aggregate  bool        `json:"aggregate,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	Example    *Example    `json:"example,omitempty"`
	URL        string      `json:"url"`
}

type Signature struct {
	Params  []Param `json:"params"`
	Returns string  `json:"returns"`
}

type Param struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Doc      string   `json:"doc,omitempty"`
	Optional bool     `json:"optional,omitempty"`
	Repeated bool     `json:"repeated,omitempty"`
	Values   []string `json:"values,omitempty"`
	Before   string   `json:"before,omitempty"`
}

type Example struct {
	SQL    string `json:"sql"`
	Result string `json:"result"`
}

var (
	heading   = regexp.MustCompile(`<h3 id="([^"]+)">`)
	paragraph = regexp.MustCompile(`(?s)<p>(.*?)</p>`)
	tag       = regexp.MustCompile(`<[^>]+>`)
	codeTag   = regexp.MustCompile(`(?s)<code>(.*?)</code>`)
	ident     = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	preBlock  = regexp.MustCompile(`(?s)<pre><code>(.*?)</code></pre>`)
	quotedCap = regexp.MustCompile(`^"([A-Z][A-Z_]*)"`)
	// typeLine is the line an argument's doc ends with, which has no full
	// stop before it.
	typeLine = regexp.MustCompile(`([^.\s])\s*\n\s*(An expression that evaluates)`)
	token    = regexp.MustCompile(`\[|\]|\(|\)|,|[^\s\[\](),]+|\s+`)
)

// parsePage reads every function of one group's page.
func parsePage(page, group string) []Function {
	var out []Function
	locs := heading.FindAllStringSubmatchIndex(page, -1)
	for i, l := range locs {
		end := len(page)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		name := page[l[2]:l[3]]
		if !ident.MatchString(name) {
			continue // an operator, as ||
		}
		body := page[l[1]:end]
		if j := strings.Index(body, "</h3>"); j >= 0 {
			body = body[j+len("</h3>"):]
		}
		if f, ok := parseFunction(name, body, group); ok {
			out = append(out, f)
		}
	}
	return out
}

// section is the HTML after a `<p><strong>title:</strong></p>` up to the next
// such heading.
func section(body, title string) string {
	marker := "<p><strong>" + title + ":</strong></p>"
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := strings.Index(rest, "<p><strong>"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func parseFunction(name, body, group string) (Function, bool) {
	upper := strings.ToUpper(name)
	f := Function{Name: upper}
	usage := body
	if i := strings.Index(body, "<p><strong>"); i >= 0 {
		usage = body[:i]
	}
	args := argumentDocs(section(body, "Arguments"))
	examples := section(body, "Examples")
	var docs, notes []string
	for _, m := range paragraph.FindAllStringSubmatch(usage, -1) {
		text := plain(m[1])
		call, desc, ok := strings.Cut(text, " - ")
		if !ok {
			continue
		}
		call = strings.TrimSpace(call)
		var params []Param
		switch {
		case call == name:
			params = []Param{}
		case strings.HasPrefix(call, name+"("):
			inner, tail, ok := between(call[len(name):])
			if !ok {
				continue
			}
			params = parseParams(inner)
			// PERCENTILE_CONT(percentage) WITHIN GROUP (ORDER BY col)
			if tail = strings.TrimSpace(tail); tail != "" {
				if n := "Followed by `" + tail + "`."; !slices.Contains(notes, n) {
					notes = append(notes, n)
				}
			}
		default:
			continue // `expr1 and expr2`, `CASE WHEN …`: no call
		}
		// current_date() and current_date are one call.
		if len(params) == 0 && len(f.Signatures) > 0 && len(f.Signatures[0].Params) == 0 {
			continue
		}
		for i := range params {
			p := &params[i]
			a := args[strings.TrimPrefix(p.Name, "DISTINCT ")]
			p.Doc = a.doc
			if len(a.values) > 0 {
				p.Values = quoteValues(a.values, examples)
			}
		}
		f.Signatures = append(f.Signatures, Signature{Params: params})
		desc = shorten(desc)
		if desc != "" && !slices.Contains(docs, desc) {
			docs = append(docs, desc)
		}
	}
	if len(f.Signatures) == 0 {
		return f, false
	}
	if len(docs) > 0 {
		f.Doc = docs[0]
		f.Notes = append(docs[1:], notes...)
	}
	switch group {
	case "window-functions":
		f.Window = "required"
	case "agg-functions":
		f.Window = "optional"
		f.Aggregate = true
	}
	f.Example = firstExample(examples)
	return f, true
}

// between is what is in the bracket text begins with, to its match, and
// what follows it.
func between(text string) (inner, tail string, ok bool) {
	depth := 0
	for i, c := range text {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return text[1:i], text[i+1:], true
			}
		}
	}
	return "", "", false
}

// keywords that stand where a comma would between two arguments, as
// SUBSTRING(str FROM pos FOR len)'s.
var separators = map[string]bool{"FROM": true, "FOR": true, "AS": true, "PLACING": true, "IN": true, "USING": true}

// parseParams reads a usage's arguments: `str, pos[, len]`,
// `input[, offset[, default]]`, `expr1, expr2, ...`, `str FROM pos[ FOR len]`.
func parseParams(inner string) []Param {
	params := []Param{}
	var cur strings.Builder
	depth, paren := 0, 0
	optional := false // the next param is in brackets
	before := ""
	flush := func() {
		name := strings.Join(strings.Fields(cur.String()), " ")
		cur.Reset()
		if name == "" {
			return
		}
		repeated := false
		// `expr...`, `arg2 ..`, and `[, str]+`'s + say the one before repeats.
		if trimmed := strings.TrimRight(name, ".+"); trimmed != name {
			name = strings.TrimSpace(trimmed)
			repeated = true
		}
		if name == "" {
			if n := len(params); n > 0 {
				params[n-1].Repeated = true
			}
			return
		}
		// expr[, expr...] is one expr, repeated.
		if n := len(params); n > 0 && repeated && (params[n-1].Name == name || strings.HasSuffix(params[n-1].Name, " "+name)) {
			params[n-1].Repeated = true
			return
		}
		params = append(params, Param{Name: name, Optional: optional, Repeated: repeated, Before: before})
		before = ""
	}
	words := token.FindAllString(inner, -1)
	for _, w := range words {
		switch {
		case paren > 0:
			if w == "(" {
				paren++
			} else if w == ")" {
				paren--
			}
			cur.WriteString(w)
		case w == "(":
			paren++
			cur.WriteString(w)
		case w == "[":
			flush()
			depth++
			optional = true
		case w == "]":
			flush()
			if depth > 0 {
				depth--
			}
			optional = depth > 0
		case w == ",":
			flush()
			optional = depth > 0
		case separators[w]:
			flush()
			before = w
			optional = depth > 0
		default:
			cur.WriteString(w)
		}
	}
	flush()
	return params
}

type argument struct {
	doc    string
	values []string
}

// argumentDocs reads the Arguments list: `name - what it is`, and the
// fixed values a nested list gives, as `"YEAR", "YYYY" - …`.
func argumentDocs(list string) map[string]argument {
	out := map[string]argument{}
	for _, item := range topItems(list) {
		own, nested := item, ""
		if i := strings.Index(item, "<ul>"); i >= 0 {
			own, nested = item[:i], item[i:]
		}
		own = typeLine.ReplaceAllString(own, "$1. $2")
		name, doc, ok := strings.Cut(plain(own), " - ")
		if !ok {
			continue
		}
		// The card ends the sentence itself.
		a := argument{doc: strings.TrimSuffix(shorten(doc), ".")}
		for _, li := range allItems(nested) {
			if m := quotedCap.FindStringSubmatch(strings.TrimSpace(plain(li))); m != nil && !slices.Contains(a.values, m[1]) {
				a.values = append(a.values, m[1])
			}
		}
		out[strings.TrimSpace(name)] = a
	}
	return out
}

// topItems are the <li>s of a list's first level, each with what it holds.
func topItems(list string) []string {
	var out []string
	depth, start := 0, -1
	for i := 0; i < len(list); i++ {
		switch {
		case strings.HasPrefix(list[i:], "<ul>"):
			depth++
		case strings.HasPrefix(list[i:], "</ul>"):
			depth--
		case strings.HasPrefix(list[i:], "<li>") && depth == 1:
			start = i + len("<li>")
		case strings.HasPrefix(list[i:], "</li>") && depth == 1 && start >= 0:
			out = append(out, list[start:i])
			start = -1
		}
	}
	return out
}

// allItems are the text of every <li> at any depth, without what they hold.
func allItems(list string) []string {
	var out []string
	for _, part := range strings.Split(list, "<li>")[1:] {
		end := len(part)
		for _, stop := range []string{"<ul>", "</li>", "<li>"} {
			if i := strings.Index(part, stop); i >= 0 && i < end {
				end = i
			}
		}
		out = append(out, part[:end])
	}
	return out
}

// quoteValues writes values as a call takes them: quoted when the
// examples quote them, as DATE_TRUNC('YEAR', ts), else bare, as
// EXTRACT(YEAR FROM ts).
func quoteValues(values []string, examples string) []string {
	ex := strings.ToUpper(html.UnescapeString(examples))
	quoted := false
	for _, v := range values {
		if strings.Contains(ex, "'"+v+"'") {
			quoted = true
			break
		}
	}
	if !quoted {
		return values
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = "'" + v + "'"
	}
	return out
}

// firstExample is the first one-line call of the examples, and the first
// line of what it returns.
func firstExample(block string) *Example {
	m := preBlock.FindStringSubmatch(block)
	if m == nil {
		return nil
	}
	lines := strings.Split(html.UnescapeString(m[1]), "\n")
	for i, l := range lines {
		sql, ok := strings.CutPrefix(l, "> ")
		if !ok || !strings.HasSuffix(sql, ";") || strings.HasPrefix(strings.ToUpper(sql), "SET ") {
			continue
		}
		res := ""
		if i+1 < len(lines) && !strings.HasPrefix(lines[i+1], "> ") {
			res = strings.TrimSpace(lines[i+1])
		}
		sql = strings.TrimSuffix(sql, ";")
		// A call alone, as the catalog writes its examples.
		if rest, ok := strings.CutPrefix(sql, "SELECT "); ok && !strings.Contains(strings.ToUpper(rest), " FROM ") {
			sql = rest
		}
		return &Example{SQL: sql, Result: strings.Join(strings.Fields(res), " ")}
	}
	return nil
}

// plain is HTML as text, its <code> as `code`, on one line.
func plain(s string) string {
	s = codeTag.ReplaceAllString(s, "`$1`")
	s = tag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// maxDoc is about as long as a card's sentence gets.
const maxDoc = 280

// shorten is a description's first sentences, up to maxDoc.
func shorten(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxDoc {
		return s
	}
	cut := 0
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '.' && s[i+1] == ' ' && i+1 <= maxDoc {
			cut = i + 1
		}
	}
	if cut == 0 {
		return s
	}
	return s[:cut]
}

package web

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// sqlOf is the SQL pane's text for a, as the TUI builds it: every section
// in one scroll, each under a "-- title" comment, highlighted, tabs as
// four spaces and without carriage returns.
func sqlOf(a *project.Action, incremental bool) sqlView {
	var b strings.Builder
	for i, sec := range a.SQLSections(incremental) {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("-- " + sec.Title + "\n" + sec.SQL)
	}
	text := strings.NewReplacer("\t", "    ", "\r", "").Replace(b.String())
	if text == "" {
		msg := frontend.NoSQLMessage(a)
		return sqlView{Lines: [][][2]string{{{"", msg}}}, Plain: []string{msg}, Empty: true}
	}
	plain := strings.Split(text, "\n")
	return sqlView{Lines: highlight("sql", text, len(plain)), Plain: plain}
}

// viewSQL is a declared view's SQL as BigQuery has it.
func viewSQL(query string) sqlView {
	text := "-- view, as BigQuery has it\n" + strings.NewReplacer("\t", "    ", "\r", "").Replace(strings.TrimRight(query, "\n"))
	plain := strings.Split(text, "\n")
	return sqlView{Lines: highlight("sql", text, len(plain)), Plain: plain}
}

// highlight splits text, lexed as lang, into n lines of tokens: each its
// chroma class (the short names of chroma's HTML formatter, "k" for a
// keyword) and its text.
func highlight(lang, text string, n int) [][][2]string {
	lines := make([][][2]string, 0, n)
	plain := func() [][][2]string {
		for _, l := range strings.Split(text, "\n") {
			lines = append(lines, [][2]string{{"", l}})
		}
		return lines[:n]
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return plain()
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, text)
	if err != nil {
		return plain()
	}
	var cur [][2]string
	for tok := it(); tok != chroma.EOF; tok = it() {
		class := tokenClass(tok.Type)
		for i, part := range strings.Split(tok.Value, "\n") {
			if i > 0 {
				lines = append(lines, cur)
				cur = nil
			}
			if part != "" {
				cur = append(cur, [2]string{class, part})
			}
		}
	}
	lines = append(lines, cur)
	for len(lines) < n {
		lines = append(lines, nil)
	}
	for i := range lines {
		if lines[i] == nil {
			lines[i] = [][2]string{}
		}
	}
	return lines[:n]
}

// tokenClass is the class of a token type, or of the nearest type above
// it that has one.
func tokenClass(t chroma.TokenType) string {
	for {
		if c, ok := chroma.StandardTypes[t]; ok && c != "" {
			return c
		}
		switch {
		case t.SubCategory() != t:
			t = t.SubCategory()
		case t.Category() != t:
			t = t.Category()
		default:
			return ""
		}
	}
}

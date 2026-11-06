package bqfuncs

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
)

func byName(t *testing.T) map[string]Func {
	t.Helper()
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Func{}
	for _, f := range all {
		if _, dup := m[f.Name]; dup {
			t.Errorf("%s is listed twice", f.Name)
		}
		m[f.Name] = f
	}
	return m
}

// cmField is a CodeMirror snippet field; with them taken out, nothing of
// the snippet may read as one.
var cmField = regexp.MustCompile(`\$\{\d+:[^{}\\]*\}`)

func TestEverySnippetIsCodeMirrors(t *testing.T) {
	for name, f := range byName(t) {
		if !strings.HasPrefix(f.Snippet, name+"(") {
			t.Errorf("%s: snippet %q does not call it", name, f.Snippet)
		}
		if rest := cmField.ReplaceAllString(f.Snippet, ""); strings.ContainsAny(rest, "{}\\") || strings.Contains(rest, "${") {
			t.Errorf("%s: snippet %q has text CodeMirror reads as a field or an escape", name, f.Snippet)
		}
		if strings.Contains(f.Snippet, "\n") || strings.Contains(f.Detail, "\n") {
			t.Errorf("%s: snippet or detail is more than a line", name)
		}
		if strings.Contains(f.Snippet, "[") && !strings.Contains(f.Snippet, "ARRAY") {
			t.Errorf("%s: snippet %q keeps an optional part", name, f.Snippet)
		}
		if f.URL == "" {
			t.Errorf("%s: no docs link", name)
		}
	}
}

func TestBothFilesAreRead(t *testing.T) {
	m := byName(t)
	catalog, borrowed := 0, 0
	for _, f := range m {
		if len(f.Signatures) > 0 {
			catalog++
		} else {
			borrowed++
		}
	}
	if catalog < 100 || borrowed < 100 {
		t.Fatalf("%d catalogued and %d borrowed functions; want over 100 of each", catalog, borrowed)
	}
}

func TestCatalogSnippets(t *testing.T) {
	m := byName(t)
	for name, want := range map[string]string{
		"DATE_TRUNC": "DATE_TRUNC(${1:date_value}, ${2:DAY})",
		"CAST":       "CAST(${1:expression} AS ${2:STRING})",
		"ROW_NUMBER": "ROW_NUMBER() OVER (PARTITION BY ${1:partition_expr} ORDER BY ${2:order_expr})",
		"ARRAY_AGG":  "ARRAY_AGG(${1:expression})",
	} {
		if got := m[name].Snippet; got != want {
			t.Errorf("%s: snippet %q, want %q", name, got, want)
		}
	}
}

func TestCatalogParams(t *testing.T) {
	m := byName(t)
	trunc := m["DATE_TRUNC"].Signatures[0]
	if g := trunc.Params[1]; len(g.Values) == 0 || g.Values[0] != "DAY" || !strings.Contains(g.Text, "One of: DAY") {
		t.Errorf("DATE_TRUNC's granularity: values %v, text %q", g.Values, g.Text)
	}
	// A snippet of one dropdown is the param's values.
	if typ := m["CAST"].Signatures[0].Params[1]; typ.Before != "AS" || len(typ.Values) < 5 {
		t.Errorf("CAST's type: before %q, values %v", typ.Before, typ.Values)
	}
	if !strings.Contains(m["CAST"].Detail, " [FORMAT format: STRING]") {
		t.Errorf("CAST's detail %q writes its optional keyword argument wrong", m["CAST"].Detail)
	}
	for name, f := range m {
		for _, s := range f.Signatures {
			label := utf16.Encode([]rune(s.Label))
			for _, p := range s.Params {
				want := p.Name
				if p.Type != "" {
					want += ": " + p.Type
				}
				if got := string(utf16.Decode(label[p.Span[0]:p.Span[1]])); got != want {
					t.Errorf("%s: span of %s reads %q", name, p.Name, got)
				}
			}
		}
	}
}

func TestBorrowedSnippets(t *testing.T) {
	m := byName(t)
	for name, want := range map[string]string{
		"ST_GEOGFROMWKB":   "ST_GEOGFROMWKB(${1:wkb_bytes_expression})",
		"ARRAY_CONCAT_AGG": "ARRAY_CONCAT_AGG(${1:expression})",
		"ML.PREDICT":       "ML.PREDICT(MODEL ${1:model_reference}, ${4:TABLE table_name})",
	} {
		if got := m[name].Snippet; got != want {
			t.Errorf("%s: snippet %q, want %q", name, got, want)
		}
	}
	agg := m["ARRAY_CONCAT_AGG"]
	if agg.Detail != "ARRAY_CONCAT_AGG(expression)" || !strings.Contains(agg.Body, "[ORDER BY key [{ASC|DESC}]") {
		t.Errorf("ARRAY_CONCAT_AGG: detail %q, body %q", agg.Detail, agg.Body)
	}
	if f := m["REGEXP_INSTR"]; f.Searched || !strings.HasPrefix(f.URL, "https://cloud.google.com/") {
		t.Errorf("REGEXP_INSTR: docs link %q, searched %v", f.URL, f.Searched)
	}
	// Most have no link of their own, and search the docs for theirs.
	if f := m["ST_AREA"]; !f.Searched || !strings.Contains(f.URL, "ST_AREA") {
		t.Errorf("ST_AREA: docs link %q, searched %v", f.URL, f.Searched)
	}
}

func TestParseSnippet(t *testing.T) {
	nodes := parseSnippet(`F(${1:a [{X|Y\}] b}, $2, ${3|P,Q|}) \$x`)
	if got := grammar(nodes); got != "F(a [{X|Y}] b, , P) $x" {
		t.Errorf("grammar %q", got)
	}
	if got := tidy(template(nodes)); got != "F(${1:a b}, ${2:}, ${3:P}) $x" {
		t.Errorf("template %q", got)
	}
}

func TestClauses(t *testing.T) {
	m := byName(t)
	for name, want := range map[string]string{
		"PIVOT":   "PIVOT(${1:SUM}(${2:value_column}) FOR ${3:column} IN (${4:values}))",
		"UNPIVOT": "UNPIVOT(${1:value_column} FOR ${2:name_column} IN (${3:columns}))",
	} {
		f := m[name]
		if !f.Clause || f.Snippet != want {
			t.Errorf("%s: clause %v, snippet %q, want %q", name, f.Clause, f.Snippet, want)
		}
		// Short: the call alone, no types or arrow.
		if strings.ContainsAny(f.Detail, ":→") {
			t.Errorf("%s: detail %q", name, f.Detail)
		}
		if ps := f.Signatures[0].Params; ps[1].Before != "FOR" || ps[2].Before != "IN" {
			t.Errorf("%s: params %+v", name, ps)
		}
	}
	if m["SUM"].Clause {
		t.Error("SUM is a clause")
	}
}

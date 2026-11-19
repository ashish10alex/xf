package sparkfuncs

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/bqfuncs"
)

func byName(t *testing.T) map[string]bqfuncs.Func {
	t.Helper()
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 400 {
		t.Fatalf("%d functions; Spark's reference has over 500", len(all))
	}
	m := map[string]bqfuncs.Func{}
	for _, f := range all {
		if _, dup := m[f.Name]; dup {
			t.Errorf("%s is listed twice", f.Name)
		}
		m[f.Name] = f
	}
	return m
}

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
		if f.Doc == "" {
			t.Errorf("%s: no doc", name)
		}
		if !strings.HasPrefix(f.URL, "https://docs.databricks.com/") && !strings.HasPrefix(f.URL, "https://spark.apache.org/docs/") {
			t.Errorf("%s: docs link %q is neither Databricks' nor Spark's", name, f.URL)
		}
	}
}

func TestSnippets(t *testing.T) {
	m := byName(t)
	for name, want := range map[string]string{
		"DATE_TRUNC":   "DATE_TRUNC(${1:'YEAR'}, ${2:ts})",
		"EXTRACT":      "EXTRACT(${1:YEAR} FROM ${2:source})",
		"CAST":         "CAST(${1:expr} AS ${2:type})",
		"ROW_NUMBER":   "ROW_NUMBER() OVER (PARTITION BY ${1:partition_expr} ORDER BY ${2:order_expr})",
		"SUBSTRING":    "SUBSTRING(${1:str}, ${2:pos})",
		"TIMESTAMPADD": "TIMESTAMPADD(${1:MICROSECOND}, ${2:value}, ${3:expr})",
		"PIVOT":        "PIVOT(${1:SUM}(${2:value_column}) FOR ${3:column} IN (${4:values}))",
	} {
		if got := m[name].Snippet; got != want {
			t.Errorf("%s: snippet %q, want %q", name, got, want)
		}
	}
}

func TestKinds(t *testing.T) {
	m := byName(t)
	for _, name := range []string{"SUM", "COUNT", "ARRAY_AGG", "PERCENTILE_APPROX"} {
		if !m[name].Aggregate {
			t.Errorf("%s is an aggregate", name)
		}
	}
	for _, name := range []string{"UPPER", "DATE_TRUNC", "ROW_NUMBER"} {
		if m[name].Aggregate {
			t.Errorf("%s is no aggregate", name)
		}
	}
	for _, name := range []string{"PIVOT", "UNPIVOT"} {
		if !m[name].Clause || !strings.Contains(m[name].URL, "databricks.com") {
			t.Errorf("%s is a clause, with Databricks' docs: %+v", name, m[name])
		}
	}
	if !strings.Contains(m["SUM"].Notes[len(m["SUM"].Notes)-1], "OVER") {
		t.Errorf("an aggregate says it takes OVER: %q", m["SUM"].Notes)
	}
}

func TestExtraTakesThePlaceOfTheCatalogs(t *testing.T) {
	m := byName(t)
	dd := m["DATEDIFF"]
	if len(dd.Signatures) != 2 || dd.Signatures[1].Params[0].Name != "unit" {
		t.Errorf("DATEDIFF has Databricks' unit form too: %+v", dd.Signatures)
	}
	unit := m["TIMESTAMPDIFF"].Signatures[0].Params[0]
	if !slices.Contains(unit.Values, "MONTH") {
		t.Errorf("TIMESTAMPDIFF's unit takes MONTH: %q", unit.Values)
	}
	if v := m["DATE_PART"].Signatures[0].Params[0].Values; !slices.Contains(v, "'YEAR'") {
		t.Errorf("DATE_PART's field is a quoted part: %q", v)
	}
}

package navigate

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/project"
)

// dbtProjects are the dbt example with its graph as dbt v2 and dbt-core
// compiled it.
func dbtProjects(t *testing.T) map[string]Project {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "dbt"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Project{}
	for _, fixture := range []string{"dbt-v2", "dbt-core"} {
		data, err := os.ReadFile(testdata(t, "examples", fixture+".json"))
		if err != nil {
			t.Fatal(err)
		}
		g, err := (&dbt.Backend{}).Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		out[fixture] = Project{Root: root, Graph: g, Dbt: true}
	}
	return out
}

func readFile(t *testing.T, p Project, rel string) (string, []byte) {
	t.Helper()
	file := filepath.Join(p.Root, rel)
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return file, src
}

// lineOf returns the 0-based line of the first line of src containing s.
func lineOf(t *testing.T, src []byte, s string) int {
	t.Helper()
	for i, l := range strings.Split(string(src), "\n") {
		if strings.Contains(l, s) {
			return i
		}
	}
	t.Fatalf("no line contains %q", s)
	return -1
}

func TestDbtDefinition(t *testing.T) {
	for name, p := range dbtProjects(t) {
		t.Run(name, func(t *testing.T) {
			sources, sourcesSrc := readFile(t, p, "models/staging/_sources.yml")
			macro, _ := readFile(t, p, "macros/cents_to_dollars.sql")
			for _, tc := range []struct {
				file, needle string
				want         []Location
			}{
				{"models/marts/fct_orders.sql", "{{ ref('stg_or|ders') }}", []Location{{File: filepath.Join(p.Root, "models/staging/stg_orders.sql")}}},
				{"models/marts/fct_orders.sql", "{{ r|ef('stg_orders') }}", []Location{{File: filepath.Join(p.Root, "models/staging/stg_orders.sql")}}},
				{"models/staging/stg_payments.sql", "source('raw', 'pay|ments')", []Location{{File: sources, Line: lineOf(t, sourcesSrc, "- name: payments"), Col: 14}}},
				{"models/staging/stg_payments.sql", "{{ cents_to_do|llars('amount') }}", []Location{{File: macro, Line: 0, Col: 9}}},
				{"models/staging/stg_payments.sql", "cents_to_dollars('am|ount')", []Location{{File: macro, Line: 0, Col: 9}}},
				// {{ this }} has nothing to jump to, nor has a CTE-less name.
				{"models/marts/fct_orders.sql", "{{ th|is }}", nil},
			} {
				file, src := readFile(t, p, tc.file)
				got := Definition(p, file, src, cursorAt(t, src, tc.needle))
				if !slices.Equal(got, tc.want) {
					t.Errorf("%s: got %+v, want %+v", tc.needle, got, tc.want)
				}
			}
		})
	}
}

// TestDbtDefinitionVersions jumps to the version a ref() asks for: the
// latest without v=.
func TestDbtDefinitionVersions(t *testing.T) {
	p := dbtProjects(t)["dbt-v2"]
	file := filepath.Join(p.Root, "models/marts/x.sql")
	for src, want := range map[string]string{
		"select * from {{ ref('customer_segments') }}":                  "customer_segments_v2.sql",
		"select * from {{ ref('customer_segments', v=1) }}":             "customer_segments_v1.sql",
		"select * from {{ ref('customer_segments', version='1') }}":     "customer_segments_v1.sql",
		"select * from {{ ref('xf_example', 'customer_segments') }}":    "customer_segments_v2.sql",
		"select * from {{ ref('other_package', 'customer_segments') }}": "",
	} {
		got := Definition(p, file, []byte(src), strings.Index(src, "customer_")+2)
		switch {
		case want == "" && len(got) != 0:
			t.Errorf("%s: got %+v, want nothing", src, got)
		case want != "" && (len(got) != 1 || filepath.Base(got[0].File) != want):
			t.Errorf("%s: got %+v, want %s", src, got, want)
		}
	}
}

// packageProject is a dbt project with an installed package that has a
// model and a macro named like the project's.
func packageProject(t *testing.T) (Project, map[string]string) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"macros/shout.sql":                      "{% macro shout(x) %}upper({{ x }}){% endmacro %}\n",
		"dbt_packages/my_pkg/macros/shout.sql":  "\n{%- macro shout(x, y=2) -%}\n  upper({{ x }})\n{%- endmacro %}\n",
		"dbt_packages/my_pkg/macros/only.sql":   "{% macro only_in_pkg() %}1{% endmacro %}\n",
		"dbt_packages/my_pkg/models/orders.sql": "select {{ shout('a') }} from {{ ref('orders') }}",
		"models/orders.sql":                     "select 1",
		"tests/generic/not_negative.sql":        "{% test not_negative(model, column_name) %}\nselect 1\n{% endtest %}\n",
	}
	for rel, text := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	model := func(pkg, file string) *project.Action {
		return &project.Action{ID: project.ActionID(pkg + ".orders"), Target: project.Target{Schema: pkg, Name: "orders"}, Kind: project.KindTable, FileName: file,
			DbtName: &project.DbtName{Package: pkg, Name: "orders", Latest: true}}
	}
	g := project.NewGraph([]*project.Action{model("app", "models/orders.sql"), model("my_pkg", "dbt_packages/my_pkg/models/orders.sql")})
	g.Package = "app"
	g.Macros = []project.Macro{
		{Name: "shout", Package: "app", FileName: "macros/shout.sql"},
		{Name: "test_not_negative", Package: "app", FileName: "tests/generic/not_negative.sql"},
		{Name: "only_in_pkg", Package: "my_pkg", FileName: "dbt_packages/my_pkg/macros/only.sql"},
		{Name: "shout", Package: "my_pkg", FileName: "dbt_packages/my_pkg/macros/shout.sql", Description: "Shouts."},
	}
	return Project{Root: root, Graph: g, Dbt: true}, files
}

// TestDbtPackages resolves names in a file's own package first, then the
// project's, as dbt does.
func TestDbtPackages(t *testing.T) {
	p, _ := packageProject(t)
	pkgModel := filepath.Join(p.Root, "dbt_packages/my_pkg/models/orders.sql")
	for _, tc := range []struct {
		file, src string
		at        string
		want      Location
	}{
		{"models/x.sql", "{{ shout('a') }}", "shout", Location{File: filepath.Join(p.Root, "macros/shout.sql"), Col: 9}},
		{"models/x.sql", "{{ my_pkg.shout('a') }}", "shout", Location{File: filepath.Join(p.Root, "dbt_packages/my_pkg/macros/shout.sql"), Line: 1, Col: 10}},
		{"models/x.sql", "{{ only_in_pkg() }}", "only", Location{File: filepath.Join(p.Root, "dbt_packages/my_pkg/macros/only.sql"), Col: 9}},
		{"dbt_packages/my_pkg/models/orders.sql", "{{ shout('a') }}", "shout", Location{File: filepath.Join(p.Root, "dbt_packages/my_pkg/macros/shout.sql"), Line: 1, Col: 10}},
		{"dbt_packages/my_pkg/models/orders.sql", "{{ ref('orders') }}", "orders", Location{File: pkgModel}},
		{"models/x.sql", "{{ ref('orders') }}", "orders", Location{File: filepath.Join(p.Root, "models/orders.sql")}},
		{"models/x.sql", "{{ ref('my_pkg', 'orders') }}", "orders", Location{File: pkgModel}},
	} {
		got := Definition(p, filepath.Join(p.Root, tc.file), []byte(tc.src), strings.Index(tc.src, tc.at)+1)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s in %s: got %+v, want %+v", tc.src, tc.file, got, tc.want)
		}
	}

	refs := MacrosAt(p, filepath.Join(p.Root, "models/x.sql"), []byte("{{ my_pkg.shout('a') }}"), 5)
	if len(refs) != 1 || refs[0].Signature != "shout(x, y=2)" || refs[0].Macro.Description != "Shouts." {
		t.Errorf("MacrosAt: got %+v", refs)
	}
	if refs := MacrosAt(p, filepath.Join(p.Root, "models/x.sql"), []byte("{{ ref('orders') }}"), 5); refs != nil {
		t.Errorf("MacrosAt on a ref: got %+v", refs)
	}
}

func TestDbtTableRefsAt(t *testing.T) {
	p := dbtProjects(t)["dbt-core"]
	file, src := readFile(t, p, "models/marts/fct_orders.sql")
	refs := TableRefsAt(p, file, src, cursorAt(t, src, "ref('stg_p|ayments')"))
	if len(refs) != 1 || refs[0].Source != SourceRef || refs[0].Target.Name != "stg_payments" || string(src[refs[0].Start:refs[0].End]) != "ref('stg_payments')" {
		t.Errorf("ref: got %+v", refs)
	}
	refs = TableRefsAt(p, file, src, cursorAt(t, src, "{{ th|is }}"))
	if len(refs) != 1 || refs[0].Source != SourceSelf || refs[0].Target.Name != "fct_orders" {
		t.Errorf("this: got %+v", refs)
	}
	file, src = readFile(t, p, "models/staging/stg_payments.sql")
	refs = TableRefsAt(p, file, src, cursorAt(t, src, "source('raw', 'pa|yments')"))
	if len(refs) != 1 || refs[0].Action == nil || refs[0].Action.Kind != project.KindSource || refs[0].Target.Schema != "raw" {
		t.Errorf("source: got %+v", refs)
	}
	if refs := TableRefsAt(p, file, src, cursorAt(t, src, "cents_to_dollars('am|ount')")); refs != nil {
		t.Errorf("macro: got %+v, want none", refs)
	}
	raw := []byte("select * from `proj.raw.orders` -- proj.raw.payments\n")
	if refs := TableRefsAt(p, file, raw, 18); len(refs) != 1 || refs[0].Source != SourceRaw || refs[0].Target.Name != "orders" {
		t.Errorf("literal: got %+v", refs)
	}
	if refs := TableRefsAt(p, file, raw, 45); refs != nil {
		t.Errorf("literal in a comment: got %+v", refs)
	}
}

// complete returns the completions at the | in src, as "label=insert"
// strings, and the text each replaces.
func complete(t *testing.T, p Project, file, src string) ([]string, string) {
	t.Helper()
	off := strings.IndexByte(src, '|')
	text := []byte(src[:off] + src[off+1:])
	var out []string
	replaced := ""
	for _, c := range Completions(p, file, text, off) {
		out = append(out, c.Label+"="+c.InsertText)
		replaced = string(text[c.Start:off])
	}
	return out, replaced
}

func TestDbtCompletions(t *testing.T) {
	p := dbtProjects(t)["dbt-v2"]
	file := filepath.Join(p.Root, "models/marts/x.sql")

	got, replaced := complete(t, p, file, "select * from {{ ref('stg_o|') }}")
	if !slices.Contains(got, "stg_orders=stg_orders") || !slices.Contains(got, "customer_segments=customer_segments") || replaced != "stg_o" {
		t.Errorf("ref(: got %v replacing %q", got, replaced)
	}
	if slices.Contains(got, "customers=customers") {
		t.Errorf("ref( offers a source: %v", got)
	}
	got, _ = complete(t, p, file, "{{ source('|")
	if !slices.Equal(got, []string{"raw=raw"}) {
		t.Errorf("source(: got %v", got)
	}
	got, _ = complete(t, p, file, `{{ source("raw", "o|")}}`)
	if !slices.Equal(got, []string{"customers=customers", "orders=orders", "payments=payments"}) {
		t.Errorf("source(raw: got %v", got)
	}
	got, replaced = complete(t, p, file, "from {{ stg|\n")
	if !slices.Contains(got, "ref('stg_orders')=ref('stg_orders') }}") || !slices.Contains(got, "source('raw', 'orders')=source('raw', 'orders') }}") ||
		!slices.Contains(got, "cents_to_dollars()=cents_to_dollars(") || replaced != "stg" {
		t.Errorf("{{ : got %v replacing %q", got, replaced)
	}
	got, _ = complete(t, p, file, "from {{| }}")
	if !slices.Contains(got, "ref('stg_orders')= ref('stg_orders')") {
		t.Errorf("{{ before }}: got %v", got)
	}
	got, _ = complete(t, p, file, "{% if cents|")
	if !slices.Equal(got, []string{"cents_to_dollars()=cents_to_dollars(", "create_cents_to_dollars_udf()=create_cents_to_dollars_udf(", "record_run_in_audit_log()=record_run_in_audit_log("}) {
		t.Errorf("{%% if: got %v", got)
	}
	for _, src := range []string{"{% mac|", "select stg|", "{{ ref('a') }} stg|", "{{ 'stg|"} {
		if got, _ := complete(t, p, file, src); got != nil {
			t.Errorf("%q: got %v, want nothing", src, got)
		}
	}

	pp, _ := packageProject(t)
	got, replaced = complete(t, pp, filepath.Join(pp.Root, "models/x.sql"), "{{ my_pkg.|")
	if !slices.Contains(got, "my_pkg.only_in_pkg()=my_pkg.only_in_pkg(") || slices.Contains(got, "test_not_negative()=test_not_negative(") || replaced != "my_pkg." {
		t.Errorf("package.: got %v replacing %q", got, replaced)
	}
}

func TestDbtDocumentSymbols(t *testing.T) {
	p := dbtProjects(t)["dbt-v2"]
	names := func(syms []Symbol) []string {
		var out []string
		for _, s := range ReferencesFirst(syms) {
			out = append(out, s.Kind.Detail()+":"+s.Name)
		}
		return out
	}
	file, src := readFile(t, p, "models/marts/fct_orders.sql")
	if got, want := names(DbtDocumentSymbols(file, src)), []string{"ref:ref('stg_orders')", "ref:ref('stg_payments')"}; !slices.Equal(got, want) {
		t.Errorf("fct_orders: got %v, want %v", got, want)
	}
	file, src = readFile(t, p, "macros/audit.sql")
	syms := DbtDocumentSymbols(file, src)
	if got, want := names(syms), []string{"macro:create_cents_to_dollars_udf()", "macro:record_run_in_audit_log()", "ref:ref('fct_orders')"}; !slices.Equal(got, want) {
		t.Errorf("audit: got %v, want %v", got, want)
	}
	if s := syms[0]; string(src[s.SelStart:s.SelEnd]) != "create_cents_to_dollars_udf" || !strings.HasSuffix(string(src[s.Start:s.End]), "{% endmacro %}") {
		t.Errorf("macro symbol bounds: %q, %q", src[s.SelStart:s.SelEnd], src[s.Start:s.End])
	}
	src = []byte("with a as (select * from {{ source('raw', 'orders') }})\nselect * from a")
	if got, want := names(DbtDocumentSymbols("x.sql", src)), []string{"ref:source('raw', 'orders')", "cte:a"}; !slices.Equal(got, want) {
		t.Errorf("cte: got %v, want %v", got, want)
	}
}

func TestJinjaCalls(t *testing.T) {
	src := []byte(`{{ dbt_utils.star(from=ref("a"), except=['x']) }} {%- set y = source('s', 't') -%} {# ref('no') #} {{ ref('b', v=2) }} {{ ref('c`)
	var got []string
	for _, tag := range jinjaTags(src) {
		for _, c := range jinjaCalls(src, tag.Start, tag.End) {
			var args []string
			for _, a := range c.Args {
				s := a.Key + "=" + a.Value
				if !a.Literal {
					s += "?"
				}
				if a.Open {
					s += "…"
				}
				args = append(args, s)
			}
			got = append(got, c.Func+"("+strings.Join(args, ",")+")")
		}
	}
	want := []string{"dbt_utils.star(from=?,except=?)", "ref(=a)", "source(=s,=t)", "ref(=b,v=2)", "ref(=c…)"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

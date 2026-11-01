package navigate

import (
	"slices"
	"strings"
	"testing"
)

// Ported from vscode-dataform-tools' src/test/suite/cteScanner.test.ts.

func cteNames(text string) []string {
	var out []string
	for _, c := range findCTEs([]byte(text)) {
		out = append(out, c.Name)
	}
	return out
}

func TestFindCTEs(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"case insensitive, RECURSIVE and column lists", "with recursive r (n) as (select 1 union all select n + 1 from r) select * from r", []string{"r"}},
		{"templates are opaque inside CTE bodies", `WITH a AS (SELECT * FROM ${ref("x", ")")}), b AS (SELECT 1) SELECT 1`, []string{"a", "b"}},
		{"nested and multiple WITH clauses", "WITH a AS (WITH inner_a AS (SELECT 1) SELECT * FROM inner_a) SELECT * FROM (WITH sub AS (SELECT 1) SELECT * FROM sub)", []string{"a", "inner_a", "sub"}},
		{"WITH OFFSET is not a CTE", "SELECT * FROM UNNEST([1]) AS x WITH OFFSET AS off", nil},
		{"comments and strings", strings.Join([]string{
			"-- WITH c1 AS (SELECT 1)",
			"# WITH c2 AS (SELECT 1)",
			"/* WITH c3 AS (SELECT 1) */",
			`SELECT 'WITH c4 AS (SELECT 1)', "WITH c5 AS (", '''WITH c6 AS (''' `,
			`WITH real AS (SELECT ')' -- )`,
			")",
			"SELECT * FROM real",
		}, "\n"), []string{"real"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cteNames(tt.sql); !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindCTEsRanges(t *testing.T) {
	slice := func(sql string, from, to int) string { return sql[from:to] }

	sql := "WITH a AS (SELECT (1)), b AS (SELECT * FROM a)\nSELECT * FROM b"
	ctes := findCTEs([]byte(sql))
	if len(ctes) != 2 || slice(sql, ctes[0].Start, ctes[0].End) != "a AS (SELECT (1))" ||
		slice(sql, ctes[1].Start, ctes[1].End) != "b AS (SELECT * FROM a)" || ctes[0].ScopeEnd != len(sql) {
		t.Errorf("comma separated: %+v", ctes)
	}

	sql = "WITH `my cte` AS (SELECT 1) SELECT * FROM `my cte`"
	ctes = findCTEs([]byte(sql))
	if len(ctes) != 1 || ctes[0].Name != "my cte" || slice(sql, ctes[0].NameStart, ctes[0].NameEnd) != "my cte" ||
		slice(sql, ctes[0].Start, ctes[0].End) != "`my cte` AS (SELECT 1)" {
		t.Errorf("backtick quoted: %+v", ctes)
	}

	sql = "SELECT 1 ${when(incremental(), `WITH inc AS (SELECT \\`x\\` FROM t -- note\n) SELECT * FROM inc`)} WITH outer_cte AS (SELECT 2) SELECT 3"
	ctes = findCTEs([]byte(sql))
	if names := cteNames(sql); !slices.Equal(names, []string{"inc", "outer_cte"}) ||
		slice(sql, ctes[0].Start, ctes[0].End) != "inc AS (SELECT \\`x\\` FROM t -- note\n)" {
		t.Errorf("template literal: %q %+v", names, ctes)
	}

	sql = "WITH a AS (SELECT * FROM t"
	if ctes = findCTEs([]byte(sql)); len(ctes) != 1 || ctes[0].End != len(sql) {
		t.Errorf("unterminated: %+v", ctes)
	}

	sql = "WITH x AS (SELECT 1) SELECT * FROM x; SELECT * FROM x"
	if ctes = findCTEs([]byte(sql)); len(ctes) != 1 || ctes[0].ScopeEnd != strings.Index(sql, ";") {
		t.Errorf("semicolon scope: %+v", ctes)
	}

	if ctes := findCTEs([]byte("SELECT `unterminated")); len(ctes) != 0 {
		t.Errorf("stray backtick: %+v", ctes)
	}
}

func TestFindCTEDefinition(t *testing.T) {
	sql := strings.Join([]string{
		"pre_operations { WITH x AS (SELECT 1) SELECT * FROM x }",
		"WITH x AS (SELECT 2), y AS (WITH x AS (SELECT 3) SELECT * FROM x)",
		"SELECT * FROM x",
	}, "\n")
	ctes := findCTEs([]byte(sql))
	tests := []struct {
		name     string
		ref      string
		offset   int
		want     int // index into ctes, -1 for none
		wantNone bool
	}{
		{"innermost enclosing scope", "x", strings.Index(sql, "FROM x)") + 5, 3, false},
		{"latest scope containing the reference, any case", "X", strings.LastIndex(sql, "x"), 1, false},
		{"pre_operations scope", "x", strings.Index(sql, "FROM x }") + 5, 0, false},
		{"unknown name", "nope", 0, -1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := findCTEDefinition([]byte(sql), tt.ref, tt.offset)
			if tt.wantNone {
				if ok {
					t.Fatalf("got %+v, want none", got)
				}
				return
			}
			if !ok || got != ctes[tt.want] {
				t.Fatalf("got %+v, want %+v", got, ctes[tt.want])
			}
		})
	}
}

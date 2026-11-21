package project

import (
	"strings"
	"testing"
)

func TestStripSQL(t *testing.T) {
	// Comments are stripped while raw/raw-bytes and triple-quoted strings terminate properly.
	for _, tc := range []struct {
		name  string
		sql   string
		opts  StripOptions
		has   []string
		lacks []string
	}{
		{
			name:  "comments only keeps strings",
			sql:   "SELECT '--keep', r'a\\', rb'b\\', br'c\\', '''--keep2''' FROM `p.d.t` -- drop\n# drop2\n/* drop3 */",
			opts:  StripOptions{},
			has:   []string{"--keep", "--keep2", "`p.d.t`"},
			lacks: []string{"drop", "drop2", "drop3"},
		},
		{
			name:  "raw bytes literal does not swallow following columns",
			sql:   `SELECT rb'\', br'\', r'\', discount FROM "marts"."orders"`,
			opts:  StripOptions{BlankSingleStrings: true},
			has:   []string{"discount", `"marts"."orders"`},
			lacks: []string{`\`},
		},
		{
			name:  "blank single and double strings keeps backticks",
			sql:   "SELECT 'event_date', \"event_date\", '''event_date''' FROM `acme.raw.events`",
			opts:  StripOptions{BlankSingleStrings: true, BlankDoubleStrings: true},
			has:   []string{"`acme.raw.events`"},
			lacks: []string{"event_date"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := StripSQL(tc.sql, tc.opts)
			for _, w := range tc.has {
				if !strings.Contains(got, w) {
					t.Errorf("StripSQL(%q) = %q, missing %q", tc.sql, got, w)
				}
			}
			for _, w := range tc.lacks {
				if strings.Contains(got, w) {
					t.Errorf("StripSQL(%q) = %q, unexpectedly contains %q", tc.sql, got, w)
				}
			}
		})
	}
}

func TestWriteDenied(t *testing.T) {
	tbl, perm, ok := WriteDenied("Access Denied: Permission bigquery.tables.updateData denied on table acme:marts.orders (or it may not exist)")
	if !ok || tbl != "acme.marts.orders" || perm != "bigquery.tables.updateData" {
		t.Fatalf("got (%q, %q, %v)", tbl, perm, ok)
	}
	if _, _, ok := WriteDenied("Unrecognized name: x"); ok {
		t.Error("matched non-write error")
	}
}

func TestDryRunQueryWithoutDML(t *testing.T) {
	q := &DryRunQuery{
		PreOps: []string{
			"DECLARE d DATE DEFAULT current_date()",
			"-- purge today\nDELETE FROM `acme.marts.t` WHERE day = d",
		},
		Query: "SELECT * FROM `acme.raw.s` WHERE day = d",
	}
	lean, ok := q.WithoutDML()
	if !ok {
		t.Fatal("expected DML to be stripped")
	}
	if strings.Contains(lean.SQL, "DELETE") || !strings.Contains(lean.SQL, "DECLARE d DATE") || !strings.Contains(lean.SQL, "SELECT * FROM") {
		t.Errorf("lean SQL = %q", lean.SQL)
	}
	if _, ok := (&DryRunQuery{PreOps: []string{"DECLARE x INT64 DEFAULT 1"}, Query: "SELECT x"}).WithoutDML(); ok {
		t.Error("expected ok=false when no pre_op is DML")
	}
}

package impact

import (
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/lineage"
)

func TestPageDataDrawsTheLedger(t *testing.T) {
	r := &Report{
		Comparison: &Comparison{HeadRef: "feat/x", BaseRef: "origin/main", MergeBase: "52863aa0c1", HeadLabel: "working tree"},
		Against:    []string{"p"},
		CheckedAt:  time.Date(2026, 10, 15, 9, 30, 0, 0, time.Local),
		AtRisk: []Table{{Table: "p.marts.orders", Type: "table", Columns: []Column{{
			Column: "discount", Change: ColumnChange{Kind: Dropped},
			Readers: []Reader{{Table: "p.fin.<margins>", Column: "discount", DependencyType: lineage.ExactCopy}},
		}}}},
		Safe: []Table{{Table: "p.marts.daily", Type: "view", Columns: []Column{}}},
	}
	d := pageData(r)
	if d.Verdict != "fix" || d.Summary != "1 to fix before merging." {
		t.Errorf("verdict %q, summary %q", d.Verdict, d.Summary)
	}
	if len(d.Rows) != 2 {
		t.Fatalf("rows %+v", d.Rows)
	}
	fix, ok := d.Rows[0], d.Rows[1]
	if fix.Number != 1 || fix.Class != "fix" || fix.Label != "Fix" || fix.Model != "<code>orders</code>" || !strings.Contains(fix.Changed, "drops <code>discount</code>") {
		t.Errorf("row 1 %+v", fix)
	}
	// A reader still to fix, its name escaped.
	if want := (webCell{HTML: "<code>fin.<wbr>&lt;margins&gt;</code> · exact copy · outside the project", ToFix: true}); len(fix.Affected) != 1 || fix.Affected[0] != want {
		t.Errorf("row 1 affected %+v, want %+v", fix.Affected, want)
	}
	if ok.Class != "ok" || ok.Label != "OK" || len(ok.Affected) != 1 || !ok.Affected[0].None {
		t.Errorf("row 2 %+v", ok)
	}
	// The reader cites its row.
	if d.ReadersTotal != 1 || len(d.Readers) != 1 || d.Readers[0].Row != 1 || d.Readers[0].Of != "<code>orders</code>" {
		t.Errorf("readers %d %+v", d.ReadersTotal, d.Readers)
	}
}

func TestPageDataWithNothingChanged(t *testing.T) {
	if d := pageData(&Report{}); d.Verdict != "ok" || len(d.Rows) != 0 {
		t.Errorf("%+v", d)
	}
}

func TestInlineHTML(t *testing.T) {
	for md, want := range map[string]string{
		"drops `a`, `b`":                        "drops <code>a</code>, <code>b</code>",
		"**`r` · derived**":                     "<strong><code>r</code> · derived</strong>",
		"+2 more in *Every reader*":             "+2 more in <em>Every reader</em>",
		"No access \\| denied <script>":         "No access | denied &lt;script&gt;",
		`"Already changed on ` + "`main`" + `"`: "&#34;Already changed on <code>main</code>&#34;",
	} {
		if got := string(inlineHTML(md)); got != want {
			t.Errorf("inlineHTML(%q) = %q, want %q", md, got, want)
		}
	}
}

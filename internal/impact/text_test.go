package impact

import (
	"strings"
	"testing"
)

func TestTextIsTheLedgerWithEveryReaderUnderItsRow(t *testing.T) {
	text := Text(markdownReport(), false, false)
	for _, want := range []string{
		"Column impact · feat/x → main\n" +
			"1 to fix before merging. 5 models compared with production schemas · 1 not checked · dataset marts · base 52863aa\n\n",
		// A word for each mark, the models lined up, no Markdown.
		"FIX  orders     drops discount, note; retypes amount (FLOAT64 → NUMERIC)\n" +
			"     ↳ fin.margins reads discount · exact copy · outside the project\n",
		// Past the five a pull request's table names.
		"     ↳ rep.r6 reads discount · exact copy · not changed here\n",
		"     ↳ readers couldn't be looked up: No access | denied\n" +
			"low  customers  deleted\n" +
			"     ↳ revenue · reads the table · probably updated\n" +
			"?    ops        not checked: operation: a dry run of a script has no schema\n" +
			"ok   daily      keeps every column\n" +
			"ok   weekly     keeps every column; adds channel, region\n\n" +
			"FIX: a column or table something still reads is gone",
		"Readers come from Dataplex lineage",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"`", "**", "|", "<", "more in", "Every reader", "🚨", "\x1b["} {
		if unwanted == "|" {
			text = strings.ReplaceAll(text, "No access | denied", "") // the error's own pipe
		}
		if strings.Contains(text, unwanted) {
			t.Errorf("%q in\n%s", unwanted, text)
		}
	}
}

func TestTextColoursOnlyWhenAsked(t *testing.T) {
	text := Text(markdownReport(), true, false)
	for _, want := range []string{"\x1b[1;31mFIX\x1b[0m", "\x1b[1;31m1 to fix before merging.\x1b[0m", "\x1b[1m↳ fin.margins reads discount"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%q", want, text)
		}
	}
}

func TestTextIsGreenOnlyWhenEveryModelWasChecked(t *testing.T) {
	ok := &Report{ChangedCount: 1, Safe: []Table{{Table: "p.marts.daily", Type: "view", Columns: []Column{}}}}
	if text := Text(ok, true, false); !strings.Contains(text, "\x1b[1;32mNothing to fix.") {
		t.Errorf("not green:\n%q", text)
	}
	ok.Unchecked = []Unchecked{{Table: "p.marts.ops", Reason: "no access"}}
	if text := Text(ok, true, false); !strings.Contains(text, "\x1b[1;33mNothing to fix.") {
		t.Errorf("not yellow with a model not checked:\n%q", text)
	}
}

func TestTextOfNewModelsSaysWhoReadsThem(t *testing.T) {
	r := &Report{ChangedCount: 2, Against: []string{"acme-prod"}, Comparison: &Comparison{HeadRef: "feat", BaseRef: "origin/develop", MergeBase: "e3b606e1"},
		New: []Table{
			{Table: "acme-prod.marts.cleaned", Type: "table", New: true, ReadBy: []string{"acme-prod.marts.offsets"}},
			{Table: "acme-prod.marts.offsets", Type: "table", New: true},
		}}
	want := "Column impact · feat → develop\n" +
		"Nothing to fix. 2 models compared with prod tables in acme-prod · dataset marts · base e3b606e\n\n" +
		"new  cleaned  new: read by offsets on this branch\n" +
		"new  offsets  new: nothing reads it yet\n"
	if got := Text(r, false, false); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTextSaysTheDefaultTargetsWereComparedUnderTheSummary(t *testing.T) {
	r := &Report{ChangedCount: 1, Against: []string{"acme-dev"}, Unset: "prodCompilerOptions",
		New: []Table{{Table: "acme-dev.marts.fresh", Type: "table", New: true}}}
	text := Text(r, false, false)
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[2], "⚠ Compared with the project's default targets (acme-dev)") {
		t.Errorf("no hint under the summary:\n%s", text)
	}
	if strings.Count(text, "default targets (") != 1 {
		t.Errorf("the hint twice:\n%s", text)
	}
}

func TestTextNamesAnUncheckedOldTableOnce(t *testing.T) {
	r := &Report{ChangedCount: 1,
		New:       []Table{{Table: "p.marts.fresh", Type: "table", New: true, RenamedFrom: "p.marts.stale"}},
		Unchecked: []Unchecked{{Table: "p.marts.stale", RenamedTo: "p.marts.fresh", Reason: "no table"}}}
	text := Text(r, false, false)
	if n := strings.Count(text, "the old table wasn't checked: no table"); n != 1 || strings.Contains(text, "nobody") {
		t.Errorf("the old table's note %d times:\n%s", n, text)
	}
	if md := Markdown(r); strings.Count(md, "the old table wasn't checked") != 1 {
		t.Errorf("Markdown:\n%s", md)
	}
}

// With attention, the text keeps the models to fix, review or look into,
// and says how many it left out; the summary still counts every model.
func TestTextWithAttentionKeepsWhatNeedsIt(t *testing.T) {
	text := Text(markdownReport(), false, true)
	for _, want := range []string{
		"1 to fix before merging. 5 models compared with production schemas",
		"FIX  orders  drops discount, note; retypes amount (FLOAT64 → NUMERIC)\n",
		"?    ops     not checked: operation: a dry run of a script has no schema\n\n",
		"Not shown: 3 more models, which keep their columns, are new, or have nothing reading what they change.\n" +
			"FIX: a column or table something still reads is gone · ?: not checked",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"customers", "ok   daily", "weekly", "low:"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("%q in\n%s", unwanted, text)
		}
	}

	ok := &Report{ChangedCount: 1, Safe: []Table{{Table: "p.marts.daily", Type: "view", Columns: []Column{}}}}
	if text := Text(ok, false, true); !strings.Contains(text, "\nNothing needs attention.\nNot shown: 1 more model,") {
		t.Errorf("all left out:\n%s", text)
	}
	if text := Text(&Report{}, false, true); !strings.Contains(text, "No table, view or incremental table changed.") {
		t.Errorf("nothing changed:\n%s", text)
	}
}

// The Markdown with attention numbers its rows afresh, and its readers
// cite them by their new numbers; a left-out row's readers go with it.
func TestAttentionMarkdownRenumbersRowsAndReaders(t *testing.T) {
	md := AttentionMarkdown(markdownReport())
	for _, want := range []string{
		"| 1 | 🚨 | `orders` |",
		"| 2 | ❔ | `ops` |",
		"<sub>Not shown: 3 more models,",
		"| `fin.margins` | `discount`, as an exact copy | 1 · `orders` |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("no %q in\n%s", want, md)
		}
	}
	for _, unwanted := range []string{"`customers`", "`weekly`", "✅", "ℹ️"} {
		if strings.Contains(md, unwanted) {
			t.Errorf("%q in\n%s", unwanted, md)
		}
	}
	l := BuildLedger(markdownReport())
	full := l.ReadersTotal
	l.Attention()
	if l.Hidden != 3 || len(l.Rows) != 2 || l.ReadersTotal != full-1 {
		t.Errorf("hidden %d, rows %d, readers %d of %d", l.Hidden, len(l.Rows), l.ReadersTotal, full)
	}
}

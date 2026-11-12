package impact

import (
	"strings"
	"testing"
)

func TestTextIsTheLedgerWithEveryReaderUnderItsRow(t *testing.T) {
	text := Text(markdownReport(), false)
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
	text := Text(markdownReport(), true)
	for _, want := range []string{"\x1b[1;31mFIX\x1b[0m", "\x1b[1;31m1 to fix before merging.\x1b[0m", "\x1b[1m↳ fin.margins reads discount"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%q", want, text)
		}
	}
}

func TestTextIsGreenOnlyWhenEveryModelWasChecked(t *testing.T) {
	ok := &Report{ChangedCount: 1, Safe: []Table{{Table: "p.marts.daily", Type: "view", Columns: []Column{}}}}
	if text := Text(ok, true); !strings.Contains(text, "\x1b[1;32mNothing to fix.") {
		t.Errorf("not green:\n%q", text)
	}
	ok.Unchecked = []Unchecked{{Table: "p.marts.ops", Reason: "no access"}}
	if text := Text(ok, true); !strings.Contains(text, "\x1b[1;33mNothing to fix.") {
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
	if got := Text(r, false); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTextSaysTheDefaultTargetsWereComparedUnderTheSummary(t *testing.T) {
	r := &Report{ChangedCount: 1, Against: []string{"acme-dev"}, Unset: "prodCompilerOptions",
		New: []Table{{Table: "acme-dev.marts.fresh", Type: "table", New: true}}}
	text := Text(r, false)
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
	text := Text(r, false)
	if n := strings.Count(text, "the old table wasn't checked: no table"); n != 1 || strings.Contains(text, "nobody") {
		t.Errorf("the old table's note %d times:\n%s", n, text)
	}
	if md := Markdown(r); strings.Count(md, "the old table wasn't checked") != 1 {
		t.Errorf("Markdown:\n%s", md)
	}
}

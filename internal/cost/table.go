package cost

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// Figure is what a run scans and costs, as a table shows it: "4.20 GiB"
// and "$0.026"; "—" for a new action's merge-base, "?" where it isn't
// known and "…" while it is being dry run, each with no cost.
type Figure struct {
	Scans string `json:"scans,omitempty"`
	Cost  string `json:"cost,omitempty"`
}

// Trends of a TableRow's change.
const (
	TrendUp   = "up"
	TrendDown = "down"
	TrendSame = "same"
	TrendNew  = "new"
)

// TableRow is an estimated action in a Table, or its total.
type TableRow struct {
	Action Action `json:"-"`
	// Name is the action's dataset.name, without the table's Dataset.
	Name string `json:"name"`
	// Marks are the numbers of the table's Footnotes about it.
	Marks []int `json:"marks,omitempty"`
	// Base is what it scanned at the merge-base, Head on the branch, and
	// Change the difference, which Trend says the way of: TrendUp,
	// TrendDown, TrendSame or TrendNew; "" when it isn't known.
	Base   Figure `json:"base"`
	Head   Figure `json:"head"`
	Change Figure `json:"change"`
	Trend  string `json:"trend,omitempty"`
	// FullRefresh is what an incremental table's full rebuild scans and
	// costs, "9.00 TiB · $56.25", when that isn't what its run does.
	FullRefresh string `json:"fullRefresh,omitempty"`
}

// Table is the estimated actions of a report as the text report and xf
// web show them: what each scanned and cost at the merge-base, on the
// branch, and the change, with their total.
type Table struct {
	// Dataset is "dataset." when every action is in that one dataset,
	// named once over the action column; else "".
	Dataset string `json:"dataset,omitempty"`
	// BaseLabel and HeadLabel name the merge-base's columns and the
	// branch's: "origin/main" and "feat/v2". BaseLabel is "" when there
	// are no merge-base or change columns.
	BaseLabel string     `json:"baseLabel,omitempty"`
	HeadLabel string     `json:"headLabel"`
	Rows      []TableRow `json:"rows"`
	Total     TableRow   `json:"total"`
	// Footnotes are the notes too long for a row, each said once under
	// the table; a row's Marks number its own, from 1.
	Footnotes []string `json:"footnotes,omitempty"`
}

// Layout lays out estimated, actions of r with an estimate, as a Table.
func Layout(r *Report, estimated []Action, o Options) Table {
	t := Table{HeadLabel: "estimate", Rows: []TableRow{}, Dataset: sharedDataset(estimated)}
	if c := r.Comparison; c != nil {
		t.HeadLabel = c.HeadRef
		if r.BaseError == "" {
			t.BaseLabel = c.BaseRef
		}
	}
	// figure is an action's estimate e; its scans carry the bound, and
	// its dollars, like the text report's, don't.
	figure := func(e Estimate) Figure { return Figure{Size(e), FormatUSD(o.USD(e.Bytes))} }
	for _, a := range estimated {
		row := TableRow{Action: a, Name: strings.TrimPrefix(impact.ShortTable(a.Name), t.Dataset), Head: figure(*a.Estimate)}
		if t.BaseLabel != "" {
			d, ok := a.Delta()
			switch {
			case a.New:
				row.Base, row.Change, row.Trend = Figure{Scans: "—"}, Figure{Scans: "new"}, TrendNew
			case r.BasePending:
				row.Base, row.Change = Figure{Scans: "…"}, Figure{Scans: "…"}
			case !ok:
				row.Base = Figure{Scans: "?"}
			default:
				row.Base = figure(*a.Base)
				row.Change, row.Trend = change(d, o)
			}
		}
		// A full refresh that scans what the run does says nothing more.
		if a.Full != nil && a.Full.Bytes != a.Estimate.Bytes {
			row.FullRefresh = Size(*a.Full) + " · " + FormatUSD(o.USD(a.Full.Bytes))
		}
		if a.FirstBuild {
			row.Marks = append(row.Marks, t.footnote(FirstBuildNote(a, o)))
		}
		if note := LeftOutNote(a); note != "" {
			row.Marks = append(row.Marks, t.footnote(note))
		}
		t.Rows = append(t.Rows, row)
	}

	total, bound := r.Total()
	t.Total = TableRow{Name: "total", Head: Figure{bound + warehouse.FormatBytes(total), bound + FormatUSD(o.USD(total))}}
	if t.BaseLabel != "" {
		base, ok := r.BaseTotal()
		switch {
		case r.BasePending:
			t.Total.Base, t.Total.Change = Figure{Scans: "…"}, Figure{Scans: "…"}
		case !ok:
			t.Total.Base = Figure{Scans: "?"}
		default:
			t.Total.Base = Figure{warehouse.FormatBytes(base), FormatUSD(o.USD(base))}
			t.Total.Change, t.Total.Trend = change(total-base, o)
		}
	}
	return t
}

// change is a difference of d bytes as a Figure, and its Trend.
func change(d int64, o Options) (Figure, string) {
	switch {
	case d == 0:
		return Figure{"0 B", "$0"}, TrendSame
	case d < 0:
		return Figure{"-" + warehouse.FormatBytes(-d), SignedUSD(o.USD(d))}, TrendDown
	}
	return Figure{"+" + warehouse.FormatBytes(d), SignedUSD(o.USD(d))}, TrendUp
}

// footnote is the number of note in t's Footnotes, added when new.
func (t *Table) footnote(note string) int {
	i := slices.Index(t.Footnotes, note)
	if i < 0 {
		t.Footnotes = append(t.Footnotes, note)
		i = len(t.Footnotes) - 1
	}
	return i + 1
}

// Superscript is n in superscript digits: "¹", "¹²".
func Superscript(n int) string {
	digits := []rune("⁰¹²³⁴⁵⁶⁷⁸⁹")
	var out []rune
	for _, c := range strconv.Itoa(n) {
		out = append(out, digits[c-'0'])
	}
	return string(out)
}

// sharedDataset is "dataset." when every action is in that one dataset,
// else "".
func sharedDataset(actions []Action) string {
	if len(actions) == 0 {
		return ""
	}
	dataset, _, ok := strings.Cut(impact.ShortTable(actions[0].Name), ".")
	if !ok {
		return ""
	}
	for _, a := range actions {
		if !strings.HasPrefix(impact.ShortTable(a.Name), dataset+".") {
			return ""
		}
	}
	return dataset + "."
}

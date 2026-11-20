package cost

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// Options are how a report is priced and judged.
type Options struct {
	// USDPerTiB is the price of a TiB scanned; 0 means BigQuery's
	// on-demand price.
	USDPerTiB float64
	// Threshold is the total bytes the branch may scan; 0 is no limit.
	Threshold int64
	// Color colours Text with ANSI escapes, for a terminal.
	Color bool
	// Attention lists, in Text and Markdown, only the actions that need a
	// look (see Action.NeedsAttention); the total still counts every one.
	Attention bool
	// GrowthOver is how many more bytes than at the merge-base a run may
	// scan before it needs a look; 0 is DefaultGrowthOver.
	GrowthOver int64
}

// DefaultGrowthOver is the growth in a run's bytes that needs a look by
// default: 1 GiB.
const DefaultGrowthOver int64 = 1 << 30

func (o Options) growthOver() int64 { return cmp.Or(max(o.GrowthOver, 0), DefaultGrowthOver) }

// NeedsAttention says a needs a look: it has no estimate, reads a
// partitioned table without a partition filter, or its run scans
// o.GrowthOver or more than at the merge-base (than nothing, when that
// isn't known).
func (a Action) NeedsAttention(o Options) bool {
	if a.Estimate == nil || len(a.Unfiltered) > 0 {
		return true
	}
	growth, ok := a.Delta()
	if !ok {
		growth = a.Estimate.Bytes
	}
	return growth >= o.growthOver()
}

// shown are the actions Text and Markdown list, and how many o.Attention
// leaves out.
func (r *Report) shown(o Options) (actions []Action, hidden int) {
	if !o.Attention {
		return r.Actions, 0
	}
	for _, a := range r.Actions {
		if a.NeedsAttention(o) {
			actions = append(actions, a)
		} else {
			hidden++
		}
	}
	return actions, hidden
}

// HiddenLine says how many actions o.Attention leaves out, "" when none.
func HiddenLine(hidden int, o Options) string {
	if hidden == 0 {
		return ""
	}
	return fmt.Sprintf("Not shown: %s, estimated with partition filters and scanning less than %s more than at the merge-base; the total counts %s.",
		plural(hidden, "more action", "more actions"), warehouse.FormatBytes(o.growthOver()), map[bool]string{true: "it", false: "them"}[hidden == 1])
}

// ANSI styles of Text.
const (
	ansiBold   = "1"
	ansiDim    = "2"
	ansiRed    = "31"
	ansiGreen  = "32"
	ansiYellow = "33"
	ansiCyan   = "36"
)

// paint wraps s in the ANSI styles, when o.Color is set and s isn't empty.
func (o Options) paint(s string, styles ...string) string {
	if !o.Color || s == "" || len(styles) == 0 {
		return s
	}
	return "\x1b[" + strings.Join(styles, ";") + "m" + s + "\x1b[0m"
}

// paintDelta colours text, a's change against the merge-base: more
// bytes red, fewer green, a new action cyan.
func (o Options) paintDelta(a Action, text string) string {
	d, ok := a.Delta()
	switch {
	case a.New:
		return o.paint(text, ansiCyan)
	case ok && d > 0:
		return o.paint(text, ansiRed)
	case ok && d < 0:
		return o.paint(text, ansiGreen)
	}
	return o.paint(text, ansiDim)
}

func (o Options) price() float64 {
	if o.USDPerTiB > 0 {
		return o.USDPerTiB
	}
	return warehouse.OnDemandUSDPerTiB
}

// USD is the price of scanning bytes.
func (o Options) USD(bytes int64) float64 {
	return float64(bytes) / (1 << 40) * o.price()
}

// FormatUSD is "$26.25", or "$0.003" under a dollar.
func FormatUSD(v float64) string {
	if v >= 1 {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.3f", v)
}

// Size is an estimate as shown: "≤ 1.20 GiB".
func Size(e Estimate) string { return e.Bound() + warehouse.FormatBytes(e.Bytes) }

// DeltaText is the action's change against the merge-base as shown: "new",
// "+1.20 GiB", "-300.00 MiB", "no change", or "" when it isn't known.
func DeltaText(a Action) string {
	if a.New {
		return "new"
	}
	d, ok := a.Delta()
	switch {
	case !ok:
		return ""
	case d == 0:
		return "no change"
	case d < 0:
		return "-" + warehouse.FormatBytes(-d)
	}
	return "+" + warehouse.FormatBytes(d)
}

// SignedUSD is "+$0.52" or "-$0.522".
func SignedUSD(v float64) string {
	if v < 0 {
		return "-" + FormatUSD(-v)
	}
	return "+" + FormatUSD(v)
}

// ChangeText is DeltaText with what it costs: "-85.51 GiB (-$0.522)".
func ChangeText(a Action, o Options) string {
	text := DeltaText(a)
	if d, ok := a.Delta(); ok && d != 0 {
		text += " (" + SignedUSD(o.USD(d)) + ")"
	}
	return text
}

// WasText is what the action scanned at the merge-base and cost there,
// "89.44 GiB · $0.546"; "" when that isn't known, or is what it scans now.
func WasText(a Action, o Options) string {
	if d, ok := a.Delta(); !ok || d == 0 {
		return ""
	}
	return Size(*a.Base) + " · " + FormatUSD(o.USD(a.Base.Bytes))
}

// FirstBuildNote is the note on a first build's estimate, with what each
// later run scans when that is known.
func FirstBuildNote(a Action, o Options) string {
	if then := IncrementalText(a, o); then != "" {
		return "first run: full build; then " + then + " a run, sized on dev's copy"
	}
	return "first run: full build; later runs not known until dev has the table"
}

// LeftOutNote is the note on an estimate dry run without the DML in its
// pre_operations: "DML in pre_operations left out: dry running it needs
// bigquery.tables.updateData on its own table". It is "" when none was.
func LeftOutNote(a Action) string {
	w := a.LeftOutDML
	if w == nil {
		return ""
	}
	table := impact.ShortTable(w.Table)
	if strings.EqualFold(w.Table, a.Name) || strings.EqualFold(w.Table, a.DryRunAs) {
		table = "its own table"
	}
	return "DML in pre_operations left out: dry running it needs " + w.Permission + " on " + table
}

// IncrementalText is what a first build's later runs scan and cost each,
// "4.20 GiB · $0.026"; "" when that isn't known.
func IncrementalText(a Action, o Options) string {
	if !a.FirstBuild || a.Incremental == nil {
		return ""
	}
	return Size(*a.Incremental) + " · " + FormatUSD(o.USD(a.Incremental.Bytes))
}

// LaterLine is "Each run after the first: 5.12 GiB | $0.031" when an
// incremental table's first build is in the total: what a run scans once
// it is built. It is "" when there is none, or the later runs of one
// aren't known.
func LaterLine(r *Report, o Options) string {
	total, bound, ok := r.Later()
	if !ok {
		return ""
	}
	return fmt.Sprintf("Each run after the first: %s%s | %s%s", bound, warehouse.FormatBytes(total), bound, FormatUSD(o.USD(total)))
}

// TotalLine is "Total scanned: 4.20 TiB | Estimated cost: $26.26".
func TotalLine(r *Report, o Options) string {
	if r.PastRuns != nil {
		return PastTotalLine(r)
	}
	total, bound := r.Total()
	return fmt.Sprintf("Total scanned: %s%s | Estimated cost: %s%s", bound, warehouse.FormatBytes(total), bound, FormatUSD(o.USD(total)))
}

// GateLine says how the report's total compares with o.Threshold; "" when
// there is none.
func GateLine(r *Report, o Options) string {
	if o.Threshold <= 0 {
		return ""
	}
	if r.PastRuns != nil {
		return "Gate not applied: there is no estimate to compare with " + warehouse.FormatBytes(o.Threshold) + ", only past runs"
	}
	total, _ := r.Total()
	if r.Exceeds(o.Threshold) {
		return fmt.Sprintf("Gate failed: %s exceeds the limit of %s", warehouse.FormatBytes(total), warehouse.FormatBytes(o.Threshold))
	}
	return fmt.Sprintf("Gate passed: %s is within the limit of %s", warehouse.FormatBytes(total), warehouse.FormatBytes(o.Threshold))
}

// UnsetHint is what a report whose dry runs read the default tables says,
// as Markdown: they may be dev tables, and the setting that reads prod's.
// It is "" when the setting is set.
func UnsetHint(r *Report) string {
	if r.PastRuns != nil {
		return pastUnsetHint(r)
	}
	switch r.Unset {
	case "":
		return ""
	case "dbtProdTarget":
		return "Dry run against the default target of your dbt profile, which may be a dev target. Set `dbtProdTarget` to dry run against prod."
	}
	return "Dry run against the project's default targets, which may be dev tables. Set `" + r.Unset + "` to dry run against prod."
}

// Explanation says in a sentence, as Markdown, what was dry run: the
// branch the actions came from, the base it was diffed against and the
// tables and project the dry runs read. It is "" when the report has no
// comparison or no action was estimated.
func Explanation(r *Report) string {
	c := r.Comparison
	if c == nil || !r.anyEstimated() {
		return ""
	}
	from := "`" + c.HeadRef + "`"
	if strings.HasPrefix(c.HeadLabel, "pushed") {
		from = "the pushed " + from
	}
	when := ", on the branch and at the merge-base,"
	if r.BaseError != "" {
		when = ""
	}
	tables := "the live tables"
	if r.Unset != "" {
		tables = "the default target tables"
	}
	where := " in BigQuery"
	if project, location, ok := strings.Cut(r.DryRunIn, " ("); ok {
		where = " in `" + project + "` (" + location
	} else if r.DryRunIn != "" {
		where = " in `" + r.DryRunIn + "`"
	}
	return "The actions came from " + from + " diffed against `" + strings.TrimPrefix(c.BaseRef, "origin/") +
		"`, and their queries" + when + " were dry run against " + tables + where + "; nothing was run."
}

// DryRunLine is where the dry runs ran and what they were compiled with,
// as the page shows them under its branch line: "dry run in acme-prod (EU)
// · compiled with prodCompilerOptions". code formats each value, such as
// in backticks for Markdown. It is "" when the report says neither.
func DryRunLine(r *Report, code func(string) string) string {
	var parts []string
	if r.DryRunIn != "" {
		parts = append(parts, "dry run in "+code(r.DryRunIn))
	}
	if r.Compiled != "" {
		parts = append(parts, "compiled with "+code(r.Compiled))
	}
	return strings.Join(parts, " · ")
}

func (r *Report) anyEstimated() bool {
	for _, a := range r.Actions {
		if a.Estimate != nil {
			return true
		}
	}
	return false
}

// heading is "feat vs origin/main @ abc1234 (working tree)".
func heading(c *impact.Comparison) string {
	return fmt.Sprintf("%s vs %s @ %s (%s)", c.HeadRef, c.BaseRef, c.MergeBase[:min(len(c.MergeBase), 7)], c.HeadLabel)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pad(s string, width int) string {
	return s + strings.Repeat(" ", max(width-utf8.RuneCountInString(s), 0))
}

func padLeft(s string, width int) string {
	return strings.Repeat(" ", max(width-utf8.RuneCountInString(s), 0)) + s
}

// waitsByAction are the tables the branch adds that each action not
// estimated waits on, by its Name.
func waitsByAction(r *Report) map[string][]string {
	out := map[string][]string{}
	for _, s := range SkippedActions(r) {
		if len(s.Waits) > 0 {
			out[s.Action.Name] = s.Waits
		}
	}
	return out
}

// Text is the report for a terminal: a table of the estimated actions,
// most bytes first, with what each scanned and cost at the merge-base, on
// the branch, and the change; their total; the actions not estimated, with
// why; and how the total compares with o.Threshold.
func Text(r *Report, o Options) string {
	if r.PastRuns != nil {
		return pastText(r, o)
	}
	var b strings.Builder
	what := o.paint(plural(len(r.Actions), "changed action", "changed actions"), ansiBold)
	if c := r.Comparison; c != nil {
		fmt.Fprintf(&b, "%s in %s\n", what, heading(c))
	} else {
		fmt.Fprintf(&b, "%s\n", what)
	}
	if line := DryRunLine(r, func(s string) string { return s }); line != "" {
		b.WriteString(o.paint(line, ansiDim) + "\n")
	}
	if len(r.Actions) == 0 {
		b.WriteString(o.paint("Nothing to estimate: the branch changes no action with a query to dry run.", ansiDim) + "\n")
	}
	if hint := UnsetHint(r); hint != "" {
		b.WriteString(o.paint("⚠ "+strings.ReplaceAll(hint, "`", ""), ansiYellow) + "\n")
	}
	actions, hidden := r.shown(o)
	if len(actions) == 0 && hidden > 0 {
		b.WriteString("Nothing needs attention.\n")
	}
	var estimated, others []Action
	for _, a := range actions {
		if a.Estimate != nil {
			estimated = append(estimated, a)
		} else {
			others = append(others, a)
		}
	}
	t := costTable(r, estimated, o)
	if len(estimated) > 0 {
		b.WriteString("\n" + t.line(t.header, o) + "\n")
		for _, row := range t.rows {
			b.WriteString(t.line(row, o) + "\n")
		}
	}
	if line := HiddenLine(hidden, o); line != "" {
		b.WriteString(o.paint(line, ansiDim) + "\n")
	}
	if len(estimated) > 0 {
		b.WriteString(o.paint(strings.Repeat("─", max(t.width(), 60)), ansiDim) + "\n")
		b.WriteString(t.line(t.total, o) + "\n")
	} else {
		b.WriteString(o.paint(strings.Repeat("─", 60), ansiDim) + "\n")
		b.WriteString(o.paint(TotalLine(r, o), ansiBold) + "\n")
	}
	if later := LaterLine(r, o); later != "" {
		b.WriteString(later + "\n")
	}
	if len(others) > 0 {
		b.WriteString("\n" + o.paint("Not estimated, so not in the total:", ansiBold) + "\n")
		nameW := 0
		for _, a := range others {
			nameW = max(nameW, utf8.RuneCountInString(impact.ShortTable(a.Name)))
		}
		waits := waitsByAction(r)
		for _, a := range others {
			why := a.WhyNot()
			if w := waits[a.Name]; len(w) > 0 {
				var short []string
				for _, t := range w {
					short = append(short, impact.ShortTable(t))
				}
				why = "waits on " + plural(len(w), "new table", "new tables") + " no run has built yet: " + strings.Join(short, ", ")
				if len(w) == 1 {
					why = "waits on a new table no run has built yet: " + short[0]
				}
			}
			b.WriteString(o.paint("?", ansiYellow) + " " + pad(impact.ShortTable(a.Name), nameW) + "  " + o.paint(why, ansiDim) + "\n")
		}
	}
	if gate := GateLine(r, o); gate != "" {
		if len(others) > 0 {
			b.WriteString("\n")
		}
		if r.Exceeds(o.Threshold) {
			b.WriteString(o.paint("✗ "+gate, ansiBold, ansiRed) + "\n")
		} else {
			b.WriteString(o.paint("✓ "+gate, ansiGreen) + "\n")
		}
	}
	return b.String()
}

// tcell is a cell of Text's table: its text and ANSI styles.
type tcell struct {
	text   string
	styles []string
}

func cellOf(text string, styles ...string) tcell { return tcell{text, styles} }

// trow is a row of Text's table: a mark, the action's name, a cell per
// figure column, and notes after them that need no column.
type trow struct {
	header bool   // the labels over the columns
	mark   string // painted already
	name   tcell
	cells  []tcell
	notes  []string // painted already
}

// ttable is Text's table: a group of two figure columns, bytes and
// dollars, per run compared (the merge-base, when known, and the branch),
// then the change, when known; and a total row under them.
type ttable struct {
	header trow
	rows   []trow
	total  trow
	groups []string // the label over each group of two columns
	widths []int    // the name's, then each figure column's
}

// costTable lays out the estimated actions of r.
func costTable(r *Report, estimated []Action, o Options) ttable {
	withBase := r.Comparison != nil && r.BaseError == ""
	head := "estimate"
	t := ttable{groups: []string{head}}
	if c := r.Comparison; c != nil {
		head = c.HeadRef
		t.groups = []string{head}
	}
	if withBase {
		t.groups = []string{r.Comparison.BaseRef, head, "change"}
	}
	for _, a := range estimated {
		row := trow{mark: o.paint("✓", ansiGreen), name: cellOf(impact.ShortTable(a.Name))}
		warn := len(a.Unfiltered) > 0
		if warn {
			row.mark = o.paint("⚠", ansiYellow)
		}
		if withBase {
			switch {
			case a.New:
				row.cells = append(row.cells, cellOf("—", ansiDim), cellOf(""))
			case a.Base != nil:
				row.cells = append(row.cells, cellOf(Size(*a.Base), ansiDim), cellOf(FormatUSD(o.USD(a.Base.Bytes)), ansiDim))
			default:
				row.cells = append(row.cells, cellOf("?", ansiDim), cellOf(""))
			}
		}
		size := cellOf(Size(*a.Estimate), ansiBold)
		if warn {
			size.styles = []string{ansiBold, ansiYellow}
		}
		row.cells = append(row.cells, size, cellOf(FormatUSD(o.USD(a.Estimate.Bytes)), ansiDim))
		if withBase {
			d, ok := a.Delta()
			style := map[bool]string{true: ansiRed, false: ansiGreen}[d > 0]
			switch {
			case a.New:
				row.cells = append(row.cells, cellOf("new", ansiCyan), cellOf(""))
			case !ok:
				row.cells = append(row.cells, cellOf(""), cellOf(""))
			case d == 0:
				row.cells = append(row.cells, cellOf("no change", ansiDim), cellOf(""))
			default:
				row.cells = append(row.cells, cellOf(DeltaText(a), style), cellOf(SignedUSD(o.USD(d)), style))
			}
		}
		if a.Full != nil {
			row.notes = append(row.notes, o.paint("(full refresh "+Size(*a.Full)+" · "+FormatUSD(o.USD(a.Full.Bytes))+")", ansiDim))
		}
		if a.FirstBuild {
			row.notes = append(row.notes, o.paint("("+FirstBuildNote(a, o)+")", ansiDim))
		}
		if note := LeftOutNote(a); note != "" {
			row.notes = append(row.notes, o.paint("("+note+")", ansiDim))
		}
		for _, d := range a.FromDev {
			row.notes = append(row.notes, o.paint("(reads "+impact.ShortTable(d.Table)+" from dev)", ansiDim))
		}
		for _, u := range a.Unfiltered {
			row.notes = append(row.notes, o.paint(fmt.Sprintf("[MISSING PARTITION FILTER: %s on %s]", u.Column, impact.ShortTable(u.Table)), ansiYellow))
		}
		t.rows = append(t.rows, row)
	}

	total, bound := r.Total()
	t.total = trow{mark: " ", name: cellOf("total", ansiBold)}
	baseTotal, baseKnown := r.BaseTotal()
	if withBase {
		if baseKnown {
			t.total.cells = append(t.total.cells, cellOf(warehouse.FormatBytes(baseTotal), ansiDim), cellOf(FormatUSD(o.USD(baseTotal)), ansiDim))
		} else {
			t.total.cells = append(t.total.cells, cellOf("?", ansiDim), cellOf(""))
		}
	}
	t.total.cells = append(t.total.cells, cellOf(bound+warehouse.FormatBytes(total), ansiBold), cellOf(bound+FormatUSD(o.USD(total)), ansiBold))
	if withBase {
		d := total - baseTotal
		switch {
		case !baseKnown:
			t.total.cells = append(t.total.cells, cellOf(""), cellOf(""))
		case d == 0:
			t.total.cells = append(t.total.cells, cellOf("no change", ansiDim), cellOf(""))
		case d < 0:
			t.total.cells = append(t.total.cells, cellOf("-"+warehouse.FormatBytes(-d), ansiGreen), cellOf(SignedUSD(o.USD(d)), ansiGreen))
		default:
			t.total.cells = append(t.total.cells, cellOf("+"+warehouse.FormatBytes(d), ansiRed), cellOf(SignedUSD(o.USD(d)), ansiRed))
		}
	}

	t.widths = make([]int, 1+2*len(t.groups))
	for _, row := range append(slices.Clone(t.rows), t.total) {
		t.widths[0] = max(t.widths[0], utf8.RuneCountInString(row.name.text))
		for i, c := range row.cells {
			t.widths[i+1] = max(t.widths[i+1], utf8.RuneCountInString(c.text))
		}
	}
	t.widths[0] = max(t.widths[0], len("action"))
	// A group's label longer than its two columns widens the first.
	for g, label := range t.groups {
		if short := utf8.RuneCountInString(label) - (t.widths[1+2*g] + 2 + t.widths[2+2*g]); short > 0 {
			t.widths[1+2*g] += short
		}
	}
	t.header = trow{header: true, mark: " ", name: cellOf("action", ansiDim)}
	for _, label := range t.groups {
		t.header.cells = append(t.header.cells, cellOf(""), cellOf(label, ansiDim))
	}
	return t
}

// width is how wide the table's figures are, without notes.
func (t ttable) width() int {
	w := 2 + t.widths[0]
	for g := range t.groups {
		w += 3 + t.widths[1+2*g] + 2 + t.widths[2+2*g]
	}
	return w
}

// line is row laid out in t's columns: the name left-aligned, each figure
// right-aligned, two spaces between a group's columns and three between
// groups. A header row's label spans its group, right-aligned.
func (t ttable) line(row trow, o Options) string {
	var b strings.Builder
	b.WriteString(row.mark + " " + o.paint(row.name.text, row.name.styles...) + strings.Repeat(" ", t.widths[0]-utf8.RuneCountInString(row.name.text)))
	for g := range t.groups {
		first, second := row.cells[2*g], row.cells[2*g+1]
		b.WriteString("   ")
		if row.header {
			// Its label is over the whole group.
			span := t.widths[1+2*g] + 2 + t.widths[2+2*g]
			b.WriteString(strings.Repeat(" ", span-utf8.RuneCountInString(second.text)) + o.paint(second.text, second.styles...))
			continue
		}
		b.WriteString(strings.Repeat(" ", t.widths[1+2*g]-utf8.RuneCountInString(first.text)) + o.paint(first.text, first.styles...))
		b.WriteString("  " + strings.Repeat(" ", t.widths[2+2*g]-utf8.RuneCountInString(second.text)) + o.paint(second.text, second.styles...))
	}
	for _, n := range row.notes {
		b.WriteString("  " + n)
	}
	return strings.TrimRight(b.String(), " ")
}

// runCell is a run's scans and cost in a Markdown table, "**4.20 GiB** ·
// $0.026" when the total counts it, "–" when not known.
func runCell(e *Estimate, counted bool, o Options) string {
	if e == nil {
		return "–"
	}
	size := Size(*e)
	if counted {
		size = "**" + size + "**"
	}
	return size + " · " + FormatUSD(o.USD(e.Bytes))
}

// cell escapes s for a Markdown table cell.
func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}

// markdownFootnote explains the figures.
const markdownFootnote = "Bytes are BigQuery dry-run estimates of each changed action's query; nothing was run. " +
	"The total counts the figure in bold: a table's run, an incremental table's incremental run, or its full build while its table isn't built yet, whose incremental run is then sized on dev's copy. Change is against the same action at the merge-base. " +
	"A missing partition filter means the query never names the partition column of a partitioned table it reads."

// Markdown is the report for a pull request.
func Markdown(r *Report, o Options) string {
	if r.PastRuns != nil {
		return pastMarkdown(r, o)
	}
	lines := []string{"### Query Cost of Branch Changes", ""}
	if c := r.Comparison; c != nil {
		lines = append(lines, fmt.Sprintf("**Branch:** `%s` vs `%s` @ `%s` | %s", c.HeadRef, c.BaseRef, c.MergeBase[:min(len(c.MergeBase), 7)], c.HeadLabel), "")
		if line := DryRunLine(r, func(s string) string { return "`" + s + "`" }); line != "" {
			lines = append(lines, strings.ToUpper(line[:1])+line[1:], "")
		}
		if explain := Explanation(r); explain != "" {
			lines = append(lines, explain, "")
		}
	}
	total, bound := r.Total()
	lines = append(lines, fmt.Sprintf("**Total scanned:** %s%s | **Estimated cost:** %s%s (on demand, at %s per TiB)",
		bound, warehouse.FormatBytes(total), bound, FormatUSD(o.USD(total)), FormatUSD(o.price())))
	if later := LaterLine(r, o); later != "" {
		lines = append(lines, "", later)
	}
	if line := FromDevLine(r); line != "" {
		lines = append(lines, "", strings.ToUpper(line[:1])+line[1:]+".")
	}
	if gate := GateLine(r, o); gate != "" {
		mark := "✅"
		if r.Exceeds(o.Threshold) {
			mark = "❌"
		}
		lines = append(lines, "", mark+" "+gate)
	}
	var estimated, others []Action
	actions, hidden := r.shown(o)
	for _, a := range actions {
		if a.Estimate != nil {
			estimated = append(estimated, a)
		} else {
			others = append(others, a)
		}
	}
	if len(r.Actions) == 0 {
		lines = append(lines, "", "Nothing to estimate: the branch changes no action with a query to dry run.")
	}
	if len(estimated) > 0 {
		lines = append(lines, "", "| Action | Type | Full build (dry run) | Incremental (dry run) | Change | Notes |", "| --- | --- | ---: | ---: | ---: | --- |")
		for _, a := range estimated {
			var notes []string
			for _, u := range a.Unfiltered {
				notes = append(notes, fmt.Sprintf("⚠️ no partition filter: `%s` of `%s`", cell(u.Column), cell(impact.ShortTable(u.Table))))
			}
			if a.FirstBuild {
				notes = append(notes, "new: its first run is a full build, counted")
			}
			if note := LeftOutNote(a); note != "" {
				notes = append(notes, cell(note))
			}
			for _, d := range a.FromDev {
				notes = append(notes, fmt.Sprintf("reads `%s` from dev", cell(impact.ShortTable(d.Table))))
			}
			if a.FileName != "" {
				notes = append(notes, "`"+cell(a.FileName)+"`")
			}
			change := ChangeText(a, o)
			if was := WasText(a, o); was != "" {
				change += ", was " + was
			}
			full, incremental, counted := a.Runs()
			lines = append(lines, fmt.Sprintf("| `%s` | %s | %s | %s | %s | %s |", cell(impact.ShortTable(a.Name)), cell(a.Type),
				runCell(full, counted == RunFull, o), runCell(incremental, counted == RunIncremental, o), change, strings.Join(notes, " · ")))
		}
	}
	if line := HiddenLine(hidden, o); line != "" {
		lines = append(lines, "", line)
	}
	if r.BaseError != "" {
		lines = append(lines, "", "Change is blank: the merge-base could not be compiled ("+cell(firstLine(r.BaseError))+").")
	}
	if len(others) > 0 {
		lines = append(lines, "", fmt.Sprintf("**Not estimated (%d)**, and not in the total:", len(others)), "")
		lines = append(lines, markdownSkipped(r)...)
	}
	lines = append(lines, "", "<sub>"+markdownFootnote+"</sub>", "")
	if hint := UnsetHint(r); hint != "" {
		lines = append(lines, "<sub>"+hint+"</sub>", "")
	}
	return strings.Join(lines, "\n")
}

// markdownSkipped formats the actions without an estimate by root cause:
//  1. Missing tables (not added on this branch) that need checking.
//  2. New tables added on this branch, with the actions and assertions
//     waiting on each one.
//  3. Other reasons (external tables, scripts, dry-run errors), grouped by
//     reason.
func markdownSkipped(r *Report) []string {
	skipped := SkippedActions(r)
	type waitingGroup struct {
		reads      string
		actions    []string
		assertions int
	}
	type otherGroup struct {
		why     string
		actions []string
	}
	var missing, columns []Skipped
	var waiting []waitingGroup
	waitingIdx := map[string]int{}
	var others []otherGroup
	otherIdx := map[string]int{}
	waitingTotal := 0

	for _, s := range skipped {
		name := "`" + impact.ShortTable(s.Action.Name) + "`"
		if s.Column != nil {
			columns = append(columns, s)
			continue
		}
		if s.Missing == "" {
			idx, ok := otherIdx[s.Why]
			if !ok {
				idx = len(others)
				otherIdx[s.Why] = idx
				others = append(others, otherGroup{why: s.Why})
			}
			if s.Action.New {
				name += " *(new)*"
			}
			others[idx].actions = append(others[idx].actions, name)
			continue
		}
		if s.New {
			waitingTotal++
			for _, t := range s.Waits {
				reads := impact.ShortTable(t)
				idx, ok := waitingIdx[reads]
				if !ok {
					idx = len(waiting)
					waitingIdx[reads] = idx
					waiting = append(waiting, waitingGroup{reads: reads})
				}
				if s.Action.Type == "assertion" {
					waiting[idx].assertions++
				} else {
					waiting[idx].actions = append(waiting[idx].actions, name)
				}
			}
			continue
		}
		missing = append(missing, s)
	}

	var lines []string
	if len(missing) > 0 {
		lines = append(lines, fmt.Sprintf("⚠️ **Reads a missing table (%d):**", len(missing)), "")
		for _, s := range missing {
			note := "not on this branch: check it"
			if s.Changed {
				note = "changed on this branch"
			}
			lines = append(lines, fmt.Sprintf("- `%s` reads `%s` *(%s)*", impact.ShortTable(s.Action.Name), impact.ShortTable(s.Missing), note))
		}
		lines = append(lines, "")
	}
	if len(waiting) > 0 {
		lines = append(lines, fmt.Sprintf("**Waits on a new table added on this branch (%d):**", waitingTotal), "")
		for _, g := range waiting {
			var deps []string
			deps = append(deps, g.actions...)
			if g.assertions > 0 {
				deps = append(deps, plural(g.assertions, "assertion", "assertions"))
			}
			lines = append(lines, fmt.Sprintf("- Waiting on `%s`: %s", g.reads, strings.Join(deps, " + ")))
		}
		lines = append(lines, "", "<sub>"+MissingHint+"</sub>", "")
	}
	if len(columns) > 0 {
		lines = append(lines, fmt.Sprintf("**Waits on a new column added on this branch (%d):**", len(columns)), "")
		for _, s := range columns {
			lines = append(lines, fmt.Sprintf("- `%s` reads `%s` of `%s`", impact.ShortTable(s.Action.Name), cell(s.Column.Column), impact.ShortTable(s.Column.Table)))
		}
		lines = append(lines, "", "<sub>"+ColumnHint+"</sub>", "")
	}
	for _, g := range others {
		lines = append(lines, fmt.Sprintf("%s (%d):", g.why, len(g.actions)), "")
		if len(g.actions) == 1 {
			lines = append(lines, "- "+g.actions[0])
		} else {
			lines = append(lines, "- "+strings.Join(g.actions, ", "))
		}
		lines = append(lines, "")
	}
	return lines
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// JSONVersion is the version of the JSON document: fields are added
// freely, and it changes when one is removed or changes meaning.
const JSONVersion = 1

type jsonEstimate struct {
	Bytes    int64   `json:"bytes"`
	Accuracy string  `json:"accuracy,omitempty"`
	USD      float64 `json:"usd"`
}

type jsonWarning struct {
	Kind   string `json:"kind"`
	Table  string `json:"table"`
	Column string `json:"column"`
}

type jsonAction struct {
	Name string `json:"name"`
	// DryRunAs is the table the dry runs read for it, when that isn't
	// Name: compiled with prodCompilerOptions, it is prod's.
	DryRunAs string `json:"dryRunAs,omitempty"`
	File     string `json:"file,omitempty"`
	Type     string `json:"type"`
	// Bytes, Accuracy and USD are null for an action that wasn't estimated;
	// Error says why.
	Bytes       *int64        `json:"bytes"`
	Accuracy    string        `json:"accuracy,omitempty"`
	USD         *float64      `json:"usd"`
	FullRefresh *jsonEstimate `json:"fullRefresh,omitempty"`
	// FirstBuild says bytes are of an incremental table's full build, as
	// its table isn't built yet.
	FirstBuild bool `json:"firstBuild,omitempty"`
	// Incremental is, with FirstBuild, what each run after the first
	// scans, sized on dev's copy of the table.
	Incremental *jsonEstimate `json:"incremental,omitempty"`
	// FromDev are tables the branch adds that were read from dev.
	FromDev []DevRead `json:"fromDev,omitempty"`
	// LeftOutDML is, when bytes were dry run without the DML in the
	// action's pre_operations, the write BigQuery refused to plan.
	LeftOutDML *Write        `json:"leftOutDML,omitempty"`
	New        bool          `json:"new"`
	Base       *jsonEstimate `json:"base,omitempty"`
	DeltaBytes *int64        `json:"deltaBytes"`
	Warnings   []jsonWarning `json:"warnings"`
	Error      string        `json:"error,omitempty"`
	// WaitsOn are, on an action not estimated as it reads tables the
	// branch adds that no run has built yet, every such table: Error
	// names only the first BigQuery didn't find.
	WaitsOn []string `json:"waitsOn,omitempty"`
	// WaitsOnColumn is, on an action not estimated as it reads a column
	// a changed action makes on this branch, that action and the column.
	WaitsOnColumn *AddedColumn `json:"waitsOnColumn,omitempty"`
}

type jsonGate struct {
	ThresholdBytes int64 `json:"thresholdBytes"`
	Exceeded       bool  `json:"exceeded"`
}

type jsonReport struct {
	Version    int                `json:"version"`
	Comparison *impact.Comparison `json:"comparison,omitempty"`
	Compiled   string             `json:"compiledWith,omitempty"`
	// DryRunIn is the project the dry runs ran in, with their location.
	DryRunIn string `json:"dryRunIn,omitempty"`
	// Unset is the setting that would pick prod's tables, when it isn't
	// set: the dry runs read what the defaults resolve to.
	Unset      string       `json:"unsetSetting,omitempty"`
	Actions    []jsonAction `json:"actions"`
	TotalBytes int64        `json:"totalBytes"`
	// TotalBound is "lower" or "upper" when the total is a bound.
	TotalBound string  `json:"totalBound,omitempty"`
	TotalUSD   float64 `json:"totalUsd"`
	// LaterBytes, LaterBound and LaterUSD are the total of each run after
	// the first (see Report.Later), when an incremental table's first build
	// is in the total.
	LaterBytes *int64   `json:"laterBytes,omitempty"`
	LaterBound string   `json:"laterBound,omitempty"`
	LaterUSD   *float64 `json:"laterUsd,omitempty"`
	// BaseTotalBytes is what the same actions scanned at the merge-base,
	// and DeltaTotalBytes how many more the branch scans: null when not
	// known (see Report.BaseTotal).
	BaseTotalBytes  *int64    `json:"baseTotalBytes"`
	DeltaTotalBytes *int64    `json:"deltaTotalBytes"`
	USDPerTiB       float64   `json:"usdPerTib"`
	Unestimated     int       `json:"unestimated"`
	Gate            *jsonGate `json:"gate,omitempty"`
	BaseError       string    `json:"baseError,omitempty"`
	CheckedAt       time.Time `json:"checkedAt"`
	// PastRuns is a Databricks report's past runs (Report.PastRuns), with
	// no actions or totals.
	PastRuns *PastRuns `json:"pastRuns,omitempty"`
}

// boundName is "lower" or "upper" for Total's bound, else "".
func boundName(bound string) string {
	switch bound {
	case "≥ ":
		return "lower"
	case "≤ ":
		return "upper"
	}
	return ""
}

// JSON is the report for scripts: a document of JSONVersion, with byte
// counts as integers.
func JSON(r *Report, o Options) ([]byte, error) {
	est := func(e *Estimate) *jsonEstimate {
		if e == nil {
			return nil
		}
		return &jsonEstimate{Bytes: e.Bytes, Accuracy: e.Accuracy, USD: o.USD(e.Bytes)}
	}
	total, bound := r.Total()
	out := jsonReport{Version: JSONVersion, Comparison: r.Comparison, Compiled: r.Compiled, DryRunIn: r.DryRunIn, Unset: r.Unset, Actions: []jsonAction{},
		TotalBytes: total, TotalBound: boundName(bound), TotalUSD: o.USD(total), USDPerTiB: o.price(), Unestimated: r.Unestimated(),
		BaseError: r.BaseError, CheckedAt: r.CheckedAt, PastRuns: r.PastRuns}
	if later, bound, ok := r.Later(); ok {
		usd := o.USD(later)
		out.LaterBytes, out.LaterBound, out.LaterUSD = &later, boundName(bound), &usd
	}
	if base, ok := r.BaseTotal(); ok {
		delta := total - base
		out.BaseTotalBytes, out.DeltaTotalBytes = &base, &delta
	}
	if o.Threshold > 0 {
		out.Gate = &jsonGate{ThresholdBytes: o.Threshold, Exceeded: r.Exceeds(o.Threshold)}
	}
	waits := waitsByAction(r)
	for _, a := range r.Actions {
		ja := jsonAction{WaitsOn: waits[a.Name], WaitsOnColumn: a.AddedColumn, Name: a.Name, DryRunAs: a.DryRunAs, File: a.FileName, Type: a.Type, New: a.New, FullRefresh: est(a.Full), Base: est(a.Base),
			FirstBuild: a.FirstBuild, Incremental: est(a.Incremental), FromDev: a.FromDev, LeftOutDML: a.LeftOutDML, Warnings: []jsonWarning{}, Error: a.Reason}
		if a.Estimate != nil {
			usd := o.USD(a.Estimate.Bytes)
			ja.Bytes, ja.Accuracy, ja.USD = &a.Estimate.Bytes, a.Estimate.Accuracy, &usd
		}
		if d, ok := a.Delta(); ok {
			ja.DeltaBytes = &d
		}
		for _, u := range a.Unfiltered {
			ja.Warnings = append(ja.Warnings, jsonWarning{Kind: "missing_partition_filter", Table: u.Table, Column: u.Column})
		}
		out.Actions = append(out.Actions, ja)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

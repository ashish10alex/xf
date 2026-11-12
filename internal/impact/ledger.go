package impact

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/lineage"
)

// ledgerReaders is how many readers a row of the ledger names before
// "+N more".
const ledgerReaders = 5

// Row kinds, in the order the ledger lists them after its severities.
const (
	RowAtRisk    = "atRisk"
	RowUnchecked = "unchecked"
	RowRenamed   = "renamed"
	RowNew       = "new"
	RowSafe      = "safe"
)

// LedgerRow is one model of the report: a changed, deleted, renamed, new
// or unchecked table.
type LedgerRow struct {
	// Kind is one of the Row kinds; Severity is what its readers make of
	// it, for a row at risk or renamed.
	Kind     string
	Severity Severity
	// Model, Changed and Affected are Markdown. Affected has an entry per
	// reader table, those still to fix in bold.
	Model    string
	Changed  string
	Affected []string
	// Number is the row's place in the ledger, from 1, which "Every
	// reader" cites. It is a position in this report, not an ID: another
	// check may number the same model differently.
	Number int

	id          int      // the row's place as it was built, before the ledger is sorted
	affectedAll []string // Affected naming every reader, for Text
}

// LedgerReader is a row of the ledger's "Every reader" table, in Markdown.
// Row is the Number of the ledger row it reads, and Of the name of the
// table it reads there: the old name, for a renamed row.
type LedgerReader struct {
	Reader, Reads, Of, Note string
	Row                     int

	rowID int
}

// Ledger is a report as one row per model, for the Markdown and the
// terminal.
type Ledger struct {
	// Verdict is the worst severity a row has, and false when no row is
	// at risk.
	Verdict      Severity
	HasVerdict   bool
	Title        string
	Summary      string
	Context      string
	Rows         []LedgerRow
	Readers      []LedgerReader
	MoreReaders  int
	Legend       []string
	Notes        []string
	ReadersTotal int
}

// Marks are the rows' marks in the Markdown, by severity or kind.
var (
	severityMarks = map[Severity]string{SeverityCritical: "🚨", SeverityWarning: "⚠️", SeverityUnknown: "❔", SeverityLow: "ℹ️"}
	kindMarks     = map[string]string{RowUnchecked: "❔", RowRenamed: "🔁", RowNew: "🆕", RowSafe: "✅"}
)

// Mark is the row's mark in the Markdown: its severity's when it is at
// risk, or a renamed pair something still reads, else its kind's.
func (r LedgerRow) Mark() string {
	if r.Kind == RowAtRisk || r.Kind == RowRenamed && r.Severity != SeverityLow {
		return severityMarks[r.Severity]
	}
	return kindMarks[r.Kind]
}

// legend explains each mark, in the order shown.
var legend = []struct{ mark, text string }{
	{"🚨", "a column or table something still reads is gone"},
	{"⚠️", "a column something reads changes type"},
	{"❔", "not checked, or its readers couldn't be looked up"},
	{"ℹ️", "changed, but nothing reads it any more"},
	{"🔁", "renamed on this branch"},
	{"🆕", "new on this branch"},
	{"✅", "keeps every column"},
}

// LegendMarks are the marks the legend explains, in order.
func LegendMarks() []string {
	out := make([]string, len(legend))
	for i, l := range legend {
		out[i] = l.mark
	}
	return out
}

// rowOrder ranks rows: by severity, then unchecked, renamed, new, safe.
func rowOrder(r LedgerRow) int {
	if r.Kind == RowAtRisk || r.Kind == RowRenamed && r.Severity != SeverityLow {
		return map[Severity]int{SeverityCritical: 0, SeverityWarning: 1, SeverityUnknown: 2, SeverityLow: 3}[r.Severity]
	}
	return map[string]int{RowUnchecked: 4, RowRenamed: 5, RowNew: 6, RowSafe: 7}[r.Kind]
}

// worse is the more urgent of two severities, as TableSeverity ranks them.
func worse(a, b Severity) Severity {
	if slices.Index(severityOrder, a) <= slices.Index(severityOrder, b) {
		return a
	}
	return b
}

// namer names tables as the ledger shows them: without their dataset when
// every model shares one, else dataset.table, and in full when in another
// project than the models.
type namer struct {
	shared  string // project.dataset every model is in, or ""
	project string
}

func newNamer(r *Report) namer {
	var tables []string
	for _, ts := range [][]Table{r.AtRisk, r.Safe, r.New} {
		for _, t := range ts {
			tables = append(tables, t.Table)
			if t.RenamedFrom != "" {
				tables = append(tables, t.RenamedFrom)
			}
		}
	}
	for _, u := range r.Unchecked {
		tables = append(tables, u.Table)
	}
	n := namer{}
	for i, t := range tables {
		parts := strings.Split(t, ".")
		if len(parts) < 3 {
			return namer{}
		}
		ds := parts[0] + "." + parts[1]
		if i == 0 {
			n.shared, n.project = ds, parts[0]
		} else if ds != n.shared {
			n.shared = ""
		}
	}
	return n
}

// dataset is the dataset every model shares, or "".
func (n namer) dataset() string {
	_, ds, _ := strings.Cut(n.shared, ".")
	return ds
}

func (n namer) name(table string) string {
	if n.shared != "" && strings.HasPrefix(table, n.shared+".") {
		return table[len(n.shared)+1:]
	}
	if p, _, _ := strings.Cut(table, "."); n.project != "" && p != n.project {
		return table
	}
	return ShortTable(table)
}

func (n namer) code(table string) string { return "`" + n.name(table) + "`" }

// readerGroup is a reader table with the columns of a row it reads.
type readerGroup struct {
	reader  Reader
	columns []string
	whole   bool // reads the whole table: the row's table is deleted or renamed
	of      string
}

// groupReaders gathers t's readers by reader table, in the order they come
// (each column's most urgent first), those still to fix first.
func groupReaders(t Table, of string) []*readerGroup {
	var groups []*readerGroup
	byTable := map[string]*readerGroup{}
	add := func(r Reader, column string, whole bool) {
		g := byTable[r.Table]
		if g == nil {
			g = &readerGroup{reader: r, whole: whole, of: of}
			byTable[r.Table] = g
			groups = append(groups, g)
		}
		if !IsSettled(r) && IsSettled(g.reader) {
			g.reader = r // the column it still has to stop reading speaks for it
		}
		if column != "" && !slices.Contains(g.columns, column) {
			g.columns = append(g.columns, column)
		}
	}
	for _, r := range t.Readers {
		add(r, "", true)
	}
	for _, c := range t.Columns {
		for _, r := range c.Readers {
			add(r, c.Column, false)
		}
	}
	slices.SortStableFunc(groups, func(a, b *readerGroup) int {
		return boolOrder(IsSettled(a.reader)) - boolOrder(IsSettled(b.reader))
	})
	return groups
}

// mergeGroups keeps one group per reader table, for a renamed pair whose
// old table and dropped columns may have the same readers: the one still
// to fix, else the whole-table read, which says where the reader went.
func mergeGroups(groups []*readerGroup) []*readerGroup {
	score := func(g *readerGroup) int { return 2*boolOrder(IsSettled(g.reader)) + boolOrder(!g.whole) }
	var out []*readerGroup
	at := map[string]int{}
	for _, g := range groups {
		i, seen := at[g.reader.Table]
		switch {
		case !seen:
			at[g.reader.Table] = len(out)
			out = append(out, g)
		case score(g) < score(out[i]):
			out[i] = g
		}
	}
	slices.SortStableFunc(out, func(a, b *readerGroup) int {
		return boolOrder(IsSettled(a.reader)) - boolOrder(IsSettled(b.reader))
	})
	return out
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

// linkText is how a reader reads what the row changes.
func linkText(g *readerGroup) string {
	if g.whole {
		return "reads the table"
	}
	return LinkLabels[g.reader.DependencyType]
}

// noteText is what the branch does about a reader; renamed says the row's
// table was renamed, so a reader that stopped naming it moved to the new
// name.
func noteText(g *readerGroup, renamed bool) string {
	r := g.reader
	if renamed && g.whole && r.ProbablyUpdated && r.Checked == "" && r.RenamedFrom == "" {
		return "moved to the new name"
	}
	return ReaderNote(r)
}

// affected is a row's "Who is affected" entries, and the "Every reader"
// rows of the same readers.
func (n namer) affected(groups []*readerGroup, many, renamed bool) ([]string, []LedgerReader) {
	var cells []string
	var list []LedgerReader
	for _, g := range groups {
		name := n.code(g.reader.Table)
		parts := []string{name}
		if many && len(g.columns) > 0 {
			parts[0] += " reads " + codeList(g.columns)
		}
		parts = append(parts, linkText(g), noteText(g, renamed))
		entry := strings.Join(parts, " · ")
		if !IsSettled(g.reader) {
			entry = "**" + entry + "**"
		}
		cells = append(cells, entry)

		reads := "the whole table"
		if !g.whole {
			reads = "—"
			if len(g.columns) > 0 {
				reads = codeList(g.columns)
			}
			reads += ", " + map[lineage.DependencyType]string{lineage.ExactCopy: "as an exact copy", lineage.Other: "derived", lineage.TableOnly: "may read"}[g.reader.DependencyType]
		}
		list = append(list, LedgerReader{Reader: name, Reads: reads, Of: n.code(g.of), Note: noteText(g, renamed)})
	}
	return cells, list
}

// capped is a copy of cells naming ledgerReaders readers at most, then
// "+N more".
func capped(cells []string) []string {
	if more := len(cells) - ledgerReaders; more > 0 {
		return append(slices.Clone(cells[:ledgerReaders]), fmt.Sprintf("+%d more in *Every reader*", more))
	}
	return slices.Clone(cells)
}

// columnChanges is what a table drops, retypes and adds, as Markdown: "drops
// `a`, `b`; retypes `c` (`INT64` → `STRING`); adds `d`".
func columnChanges(t Table, base string) []string {
	var drops, retypes, onBase []string
	for _, c := range t.Columns {
		text := "`" + c.Column + "`"
		if c.Change.Kind == TypeChanged {
			text += " (`" + c.Change.From + "` → `" + c.Change.To + "`)"
		}
		switch {
		case c.Change.OnBase:
			onBase = append(onBase, text)
		case c.Change.Kind == Dropped:
			drops = append(drops, text)
		default:
			retypes = append(retypes, text)
		}
	}
	var parts []string
	if len(drops) > 0 {
		parts = append(parts, "drops "+strings.Join(drops, ", "))
	}
	if len(retypes) > 0 {
		parts = append(parts, "retypes "+strings.Join(retypes, ", "))
	}
	if len(onBase) > 0 {
		parts = append(parts, "already changed on `"+base+"`: "+strings.Join(onBase, ", "))
	}
	if len(t.Columns) == 0 {
		parts = append(parts, "keeps every column")
	}
	if len(t.Added) > 0 {
		parts = append(parts, "adds "+codeList(t.Added))
	}
	return parts
}

// readersError is the first lookup failure among t's readers, or "".
func readersError(t Table) string {
	if t.ReadersError != "" {
		return t.ReadersError
	}
	for _, c := range t.Columns {
		if c.ReadersError != "" && !c.Change.OnBase {
			return c.ReadersError
		}
	}
	return ""
}

// BuildLedger lays r out as one row per model, a rename as one row.
func BuildLedger(r *Report) Ledger {
	n := newNamer(r)
	base := BaseName(r.Comparison)
	var rows []LedgerRow
	var every []LedgerReader

	row := func(kind string, sev Severity, model string, changed []string, tables []Table, of []string, renamed bool) {
		var all []*readerGroup
		many := false
		for i, t := range tables {
			all = append(all, groupReaders(t, of[i])...)
			many = many || len(t.Columns) > 1
		}
		groups := mergeGroups(all)
		full, list := n.affected(groups, many, renamed)
		cells := capped(full)
		for _, t := range tables {
			if err := readersError(t); err != "" {
				cells = append(cells, "❔ readers couldn't be looked up: "+cell(err))
				full = append(full, cells[len(cells)-1])
			}
		}
		switch {
		case len(cells) > 0:
		case kind == RowNew || kind == RowSafe:
			cells, full = []string{"—"}, []string{"—"} // nothing it had is gone: its readers weren't looked up
		default:
			cells, full = []string{"nobody"}, []string{"nobody"}
		}
		id := len(rows)
		for i := range list {
			list[i].rowID = id
		}
		every = append(every, list...)
		rows = append(rows, LedgerRow{Kind: kind, Severity: sev, Model: model, Changed: strings.Join(changed, "; "), Affected: cells, affectedAll: full, id: id})
	}

	// A renamed action's two halves: its old table (at risk, or not
	// checked) and its new one (anywhere).
	oldAtRisk := map[string]Table{}
	for _, t := range r.AtRisk {
		if t.Deleted && t.RenamedTo != "" {
			oldAtRisk[t.Table] = t
		}
	}
	oldUnchecked := map[string]Unchecked{}
	for _, u := range r.Unchecked {
		if u.RenamedTo != "" {
			oldUnchecked[u.Table] = u
		}
	}
	paired := map[string]bool{}
	// renameRow adds the row of t's rename, if t is the new half of one:
	// changed is what it says t changes, after "renamed".
	renameRow := func(t Table, changed []string) bool {
		if t.RenamedFrom == "" {
			return false
		}
		old, atRisk := oldAtRisk[t.RenamedFrom]
		u, unchecked := oldUnchecked[t.RenamedFrom]
		if !atRisk && !unchecked {
			return false
		}
		paired[t.RenamedFrom] = true
		model := n.code(t.RenamedFrom) + " → " + n.code(t.Table)
		tables, of := []Table{t}, []string{t.Table}
		if t.FromOld {
			of[0] = t.RenamedFrom
		}
		sev := TableSeverity(t)
		if atRisk {
			tables, of = append(tables, old), append(of, old.Table)
			sev = worse(sev, TableSeverity(old))
		}
		row(RowRenamed, sev, model, []string{strings.Join(append([]string{"renamed"}, changed...), ", ")}, tables, of, true)
		if unchecked {
			last := &rows[len(rows)-1]
			note := "❔ the old table wasn't checked: " + cell(u.Reason)
			nobody := func(s string) bool { return s == "nobody" }
			last.Affected = append(slices.DeleteFunc(last.Affected, nobody), note)
			last.affectedAll = append(slices.DeleteFunc(last.affectedAll, nobody), note)
		}
		return true
	}

	for _, t := range r.AtRisk {
		if t.Deleted && t.RenamedTo != "" || renameRow(t, columnChanges(t, base)) {
			continue
		}
		changed := columnChanges(t, base)
		if t.Deleted {
			changed = []string{"deleted"}
		}
		if t.New {
			changed = append([]string{"new, its table is already there"}, changed...)
		}
		row(RowAtRisk, TableSeverity(t), n.code(t.Table), changed, []Table{t}, []string{t.Table}, false)
	}
	for _, t := range r.New {
		var compared []string // a renamed one not compared has no columns to tell of
		if t.FromOld || t.Exists {
			compared = columnChanges(t, base)
		}
		if renameRow(t, compared) {
			continue
		}
		changed := []string{"new: nothing reads it yet"}
		if len(t.ReadBy) > 0 {
			var by []string
			for _, r := range t.ReadBy {
				by = append(by, n.code(r))
			}
			changed = []string{"new: read by " + strings.Join(by, ", ") + " on this branch"}
		}
		if t.Exists {
			changed = append([]string{"new, its table is already there"}, columnChanges(t, base)...)
		}
		row(RowNew, SeverityLow, n.code(t.Table), changed, nil, nil, false)
	}
	for _, t := range r.Safe {
		if renameRow(t, columnChanges(t, base)) {
			continue
		}
		row(RowSafe, SeverityLow, n.code(t.Table), columnChanges(t, base), nil, nil, false)
	}
	for _, u := range r.Unchecked {
		if u.RenamedTo != "" && paired[u.Table] {
			continue
		}
		if renameRow(Table{Table: u.Table, RenamedFrom: u.RenamedFrom}, []string{"not checked: " + cell(u.Reason)}) {
			continue
		}
		rows = append(rows, LedgerRow{id: len(rows), Kind: RowUnchecked, Severity: SeverityUnknown, Model: n.code(u.Table),
			Changed: "not checked: " + cell(u.Reason), Affected: []string{"—"}})
	}
	// An old table paired with nothing (its new half isn't in the report)
	// is a deleted one.
	for _, t := range r.AtRisk {
		if t.Deleted && t.RenamedTo != "" && !paired[t.Table] {
			row(RowAtRisk, TableSeverity(t), n.code(t.Table), []string{"renamed to " + n.code(t.RenamedTo)}, []Table{t}, []string{t.Table}, true)
		}
	}
	slices.SortStableFunc(rows, func(a, b LedgerRow) int { return rowOrder(a) - rowOrder(b) })
	number := map[int]int{}
	for i := range rows {
		rows[i].Number = i + 1
		number[rows[i].id] = i + 1
	}
	for i := range every {
		every[i].Row = number[every[i].rowID]
	}
	// By row, as the ledger reads; within a row, as they were (readers
	// still to fix first).
	slices.SortStableFunc(every, func(a, b LedgerReader) int { return a.Row - b.Row })

	l := Ledger{Rows: rows, ReadersTotal: len(every)}
	l.Readers = every
	if more := len(every) - MarkdownReaderList; more > 0 {
		l.Readers, l.MoreReaders = every[:MarkdownReaderList], more
	}

	fix, review, unknown, unchecked := 0, 0, 0, 0
	for _, row := range rows {
		switch {
		case row.Kind == RowUnchecked:
			unchecked++
		case row.Kind != RowAtRisk && row.Kind != RowRenamed:
		case row.Severity == SeverityCritical:
			fix++
		case row.Severity == SeverityWarning:
			review++
		case row.Severity == SeverityUnknown:
			unknown++
		}
	}
	l.Verdict, l.HasVerdict = Worst(r)
	var todo []string
	if fix > 0 {
		todo = append(todo, fmt.Sprintf("%d to fix", fix))
	}
	if review > 0 {
		todo = append(todo, fmt.Sprintf("%d to review", review))
	}
	switch {
	case len(todo) > 0:
		l.Summary = strings.Join(todo, ", ") + " before merging."
	case unknown > 0:
		l.Summary = fmt.Sprintf("The readers of %s couldn't be looked up.", plural(unknown, "model"))
	default:
		l.Summary = "Nothing to fix."
	}

	l.Title = "Column impact"
	if c := r.Comparison; c != nil {
		l.Title += " · `" + c.HeadRef + "` → `" + strings.TrimPrefix(c.BaseRef, "origin/") + "`"
	}
	against := "prod tables"
	if r.Unset != "" {
		against = "the default targets"
	}
	if len(r.Against) > 0 {
		against += " in " + codeList(r.Against)
	} else if r.Unset == "" {
		against = "production schemas"
	}
	ctx := []string{plural(len(rows), "model") + " compared with " + against}
	if unchecked > 0 {
		ctx = append(ctx, fmt.Sprintf("%d not checked", unchecked))
	}
	if ds := n.dataset(); ds != "" && len(rows) > 0 {
		ctx = append(ctx, "dataset `"+ds+"`")
	}
	if c := r.Comparison; c != nil && c.MergeBase != "" {
		ctx = append(ctx, "base `"+c.MergeBase[:min(len(c.MergeBase), 7)]+"`")
	}
	if c := r.Comparison; c != nil && strings.HasPrefix(c.HeadLabel, "pushed") {
		ctx = append(ctx, c.HeadLabel)
	}
	l.Context = strings.Join(ctx, " · ")

	used := map[string]bool{}
	for _, row := range rows {
		used[row.Mark()] = true
	}
	for _, e := range legend {
		if used[e.mark] {
			l.Legend = append(l.Legend, e.mark+" "+e.text)
		}
	}
	if len(every) > 0 {
		l.Notes = append(l.Notes, "Readers come from Dataplex lineage of prod runs in the last 30 days, about 2 h behind. "+
			"A changed reader that still names a dropped column is dry run against the branch's table: a pass means it no longer reads it.")
	}
	if note := OnBaseNote(r); note != "" {
		l.Notes = append(l.Notes, note)
	}
	if hint := UnsetHint(r); hint != "" {
		l.Notes = append(l.Notes, hint)
	}
	return l
}

// Markdown is the report for a pull request: a title and summary, then one
// row per model with what it changes and who that affects, then every
// reader.
func Markdown(r *Report) string {
	l := BuildLedger(r)
	lines := []string{"### " + l.Title, "", "**" + l.Summary + "** " + l.Context}
	if explain := explanation(r, len(l.Rows)); explain != "" {
		lines = append(lines, "", explain)
	}
	if len(l.Rows) == 0 {
		lines = append(lines, "", "No table, view or incremental table changed.")
	} else {
		// Bare numbers: #1 would link to issue 1 on GitHub and GitLab.
		lines = append(lines, "", "| # | | Model | What changed | Who is affected |", "| --: | :-: | --- | --- | --- |")
		for _, row := range l.Rows {
			lines = append(lines, fmt.Sprintf("| %d | %s | %s | %s | %s |", row.Number, row.Mark(), row.Model, row.Changed, strings.Join(row.Affected, "<br>")))
		}
		lines = append(lines, "", "<sub>"+strings.Join(l.Legend, " · ")+"</sub>")
	}
	if l.ReadersTotal > 0 {
		lines = append(lines, "", fmt.Sprintf("<details><summary>Every reader (%d)</summary>", l.ReadersTotal), "",
			"| Reader | Reads | Of | On this branch |", "| --- | --- | --- | --- |")
		for _, e := range l.Readers {
			lines = append(lines, fmt.Sprintf("| %s | %s | %d · %s | %s |", e.Reader, e.Reads, e.Row, e.Of, e.Note))
		}
		if l.MoreReaders > 0 {
			lines = append(lines, fmt.Sprintf("| +%d more | | | |", l.MoreReaders))
		}
		lines = append(lines, "", "</details>")
	}
	for _, note := range l.Notes {
		lines = append(lines, "", "<sub>"+note+"</sub>")
	}
	return strings.Join(lines, "\n")
}

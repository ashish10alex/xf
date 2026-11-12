// Package impact works out the column impact of a branch: for every table,
// view and incremental table it changes, the columns it drops or retypes
// against the table in BigQuery and who reads them, and the readers of the
// tables it deletes. It ports vscode-dataform-tools' column impact
// (src/shared/columnLineage/impactRules.ts, impactSummary.ts and
// src/columnLineage/changeImpact.ts), which stays the reference: keep the
// two in step, the Markdown and the test cases included.
//
// The extension compares with each action's Prod Target, from a compile
// with prodCompilerOptions. xf compiles with no compiler options, so its
// targets are the project's default tables; with prodCompilerOptions set it
// compiles once more with them and compares with those (Deps.ProdTarget).
// Either way the report names the projects it compared with, where the
// extension says "production schemas" and "prod column".
//
// The extension lists an action the base doesn't have as new, and says when
// it compared with the default targets because prodCompilerOptions isn't
// set (Report.Unset), as xf does: the parity test covers both. xf goes
// further in one place: a change the base branch already made, which the
// table in BigQuery is behind on, is marked as the base's and counts as low
// (ColumnChange.OnBase). A report with no such change reads as the
// extension's.
package impact

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/lineage"
)

const (
	// MarkdownReaderList is how many readers the "Every reader" table
	// lists before "+N more".
	MarkdownReaderList = 20
)

// Change kinds.
const (
	Dropped     = "dropped"
	TypeChanged = "typeChanged"
)

// ColumnChange is how a column differs from the table in BigQuery: dropped,
// or retyped From one type To another.
type ColumnChange struct {
	Kind string `json:"kind"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// OnBase: the action at the merge-base already made this change, so it
	// isn't this branch's: the table in BigQuery is behind the base branch.
	// It counts as low whoever reads the column.
	OnBase bool `json:"onBase,omitempty"`
}

// ColumnImpact is a changed column, named as the table in BigQuery spells it.
type ColumnImpact struct {
	Column string
	Change ColumnChange
}

// typeAliases maps legacy SQL and alias type names to the standard ones.
var typeAliases = map[string]string{
	"INTEGER":    "INT64",
	"FLOAT":      "FLOAT64",
	"BOOLEAN":    "BOOL",
	"RECORD":     "STRUCT",
	"DECIMAL":    "NUMERIC",
	"BIGDECIMAL": "BIGNUMERIC",
}

func normaliseType(t string) string {
	upper := strings.ToUpper(t)
	return cmp.Or(typeAliases[upper], upper)
}

// DiffSchemas is the top-level columns prod has that the dry run no longer
// produces, and those whose type changed. Names compare case-insensitively,
// as in BigQuery. Nested fields and mode changes are left out.
func DiffSchemas(dev, prod []bigquery.Field) []ColumnImpact {
	byName := map[string]bigquery.Field{}
	for _, f := range dev {
		byName[strings.ToLower(f.Name)] = f
	}
	var out []ColumnImpact
	for _, p := range prod {
		d, ok := byName[strings.ToLower(p.Name)]
		switch {
		case !ok:
			out = append(out, ColumnImpact{p.Name, ColumnChange{Kind: Dropped}})
		case normaliseType(d.Type) != normaliseType(p.Type):
			out = append(out, ColumnImpact{p.Name, ColumnChange{Kind: TypeChanged, From: normaliseType(p.Type), To: normaliseType(d.Type)}})
		}
	}
	return out
}

// Reader is a table that reads a changed column or a deleted table.
type Reader struct {
	Table string `json:"table"`
	// Column is "" when Dataplex only has a table-level link.
	Column         string                 `json:"column,omitempty"`
	DependencyType lineage.DependencyType `json:"dependencyType"`
	// FilePath is the reader's file when it is an action in this project.
	FilePath string `json:"filePath,omitempty"`
	// ChangedOnBranch is set when this branch changes the reader too.
	ChangedOnBranch bool `json:"changedOnBranch,omitempty"`
	// ProbablyUpdated: changed on this branch, and its new SQL no longer
	// names the column (or the deleted table).
	ProbablyUpdated bool `json:"probablyUpdated,omitempty"`
	// StillNamed: changed on this branch, and its new SQL still names it.
	StillNamed bool `json:"stillNamed,omitempty"`
	// DeletedOnBranch: removed on this branch, so it stops reading anything.
	DeletedOnBranch bool `json:"deletedOnBranch,omitempty"`
	// RenamedFrom is set on a reader this branch renames: Table is its new
	// name, and it is judged by its new SQL.
	RenamedFrom string `json:"renamedFrom,omitempty"`
	// Checked is what a dry run of a changed reader that still names a
	// dropped column found, reading the changed table as the branch builds
	// it: CheckedClear or CheckedReads; "" when it wasn't dry run or there
	// was no telling.
	Checked string `json:"checked,omitempty"`
}

// Column is a dropped or retyped column and who reads it.
type Column struct {
	Column       string       `json:"column"`
	Change       ColumnChange `json:"change"`
	Readers      []Reader     `json:"readers"`
	ReadersError string       `json:"readersError,omitempty"`
}

// Table is a changed or deleted table.
type Table struct {
	Table    string `json:"table"`
	FileName string `json:"fileName,omitempty"`
	Type     string `json:"type"`
	// Deleted: removed on this branch, so every reader loses it.
	Deleted bool `json:"deleted,omitempty"`
	// Columns are the dropped and retyped columns, most-read first.
	Columns []Column `json:"columns"`
	// Readers are a deleted table's readers, at table level.
	Readers      []Reader `json:"readers,omitempty"`
	ReadersError string   `json:"readersError,omitempty"`
	// Added are the columns only the branch has; nothing reads them yet.
	Added []string `json:"added,omitempty"`
	// New: an action the base doesn't have.
	New bool `json:"new,omitempty"`
	// ReadBy are, on a new table, the actions that read it, assertions
	// aside: each is new or changed on the branch too, as the base has no
	// table for it to read.
	ReadBy []string `json:"readBy,omitempty"`
	// Exists is set on a new action whose table is already in BigQuery,
	// and so was compared: a run of the branch built it, or an action
	// deleted earlier is back.
	Exists bool `json:"exists,omitempty"`
	// RenamedFrom is set on a new action whose file is a rename of the
	// file of the action of this table, which the branch deletes.
	RenamedFrom string `json:"renamedFrom,omitempty"`
	// FromOld: a renamed action whose own table isn't in BigQuery yet was
	// compared with the table of RenamedFrom.
	FromOld bool `json:"fromOld,omitempty"`
	// RenamedTo is set on a deleted table whose action was renamed to the
	// action of this table: readers still on the old name keep reading a
	// table nothing updates.
	RenamedTo string `json:"renamedTo,omitempty"`
}

// Unchecked is a table that couldn't be compared, and why.
type Unchecked struct {
	Table    string `json:"table"`
	FileName string `json:"fileName,omitempty"`
	Reason   string `json:"reason"`
	// RenamedFrom and RenamedTo are as for Table: the table is one half of
	// a rename.
	RenamedFrom string `json:"renamedFrom,omitempty"`
	RenamedTo   string `json:"renamedTo,omitempty"`
}

// Comparison is what was compared.
type Comparison struct {
	HeadRef string `json:"headRef"`
	// BaseRef is e.g. origin/main.
	BaseRef   string `json:"baseRef"`
	MergeBase string `json:"mergeBase"`
	// HeadLabel is "working tree", or "pushed commit abc1234".
	HeadLabel string `json:"headLabel"`
}

// Report is the column impact of a branch.
type Report struct {
	Comparison *Comparison `json:"comparison,omitempty"`
	// ChangedCount is the changed or deleted tables, views and
	// incremental tables.
	ChangedCount int `json:"changedCount"`
	// AtRisk are the tables with dropped or retyped columns, or deleted,
	// most-read first.
	AtRisk []Table `json:"atRisk"`
	// Safe are the changed tables whose columns all survive.
	Safe []Table `json:"safe"`
	// New are the new actions with nothing to lose: no table in BigQuery
	// yet, or one (Exists) whose columns all survive. A new action that
	// drops or retypes a column of its table is in AtRisk.
	New       []Table     `json:"new"`
	Unchecked []Unchecked `json:"unchecked"`
	// Against are the projects of the tables the changes were compared
	// with, sorted.
	Against []string `json:"against,omitempty"`
	// Unset is "prodCompilerOptions" when that isn't set anywhere, not even
	// to "": the changes were compared with the default targets, which may
	// be dev tables.
	Unset string `json:"unset,omitempty"`
	// DryRunIn is the project the branch's models were dry run in, "" when
	// they ran in more than one.
	DryRunIn  string    `json:"dryRunIn,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	// Signature is what was checked (see Signature).
	Signature string `json:"signature"`
	// Stale is set when the branch has changed since: what it changes, or
	// the SQL of what it changes.
	Stale bool `json:"stale,omitempty"`
}

// Progress is how far a check got.
type Progress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// ReaderContext is what annotating readers needs.
type ReaderContext struct {
	// ResolveFile is a table's file when it is an action in this project.
	ResolveFile func(table string) string
	// Changed are the tables this branch changes.
	Changed map[string]bool
	// SQLOf is the compiled SQL of a changed table, and whether it has one.
	SQLOf func(table string) (string, bool)
	// ToProd is the prod table of a reader that is a dev run of an action
	// in this project, or of one this branch deletes; nil keeps readers as
	// they are.
	ToProd func(table string) string
	// Deleted are the tables of the actions this branch deletes.
	Deleted map[string]bool
	// IsAssertion says whether a table is an assertion of this project;
	// nil means none is.
	IsAssertion func(table string) bool
	// Renamed are the new tables of the deleted ones this branch renames,
	// by the old.
	Renamed map[string]string
}

func isIdentChar(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// Mentions reports whether sql names identifier as a whole word,
// case-insensitively as BigQuery compares column names. Only a hint: an
// alias or a SELECT * can make it wrong either way.
func Mentions(sql, identifier string) bool {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(identifier))
	for from := 0; from <= len(sql); {
		loc := re.FindStringIndex(sql[from:])
		if loc == nil {
			return false
		}
		start, end := from+loc[0], from+loc[1]
		if (start == 0 || !isIdentChar(sql[start-1])) && (end == len(sql) || !isIdentChar(sql[end])) {
			return true
		}
		from = start + 1
	}
	return false
}

// AnnotateReader is a reader as the report shows it. name is what the
// reader would have to stop naming: the column for a dropped or retyped
// column, the table name for a deleted table.
func AnnotateReader(link lineage.ColumnLink, name string, ctx ReaderContext) Reader {
	table := link.Table
	if ctx.ToProd != nil {
		table = ctx.ToProd(table)
	}
	r := Reader{Table: table, Column: link.Column, DependencyType: link.DependencyType}
	if to := ctx.Renamed[table]; to != "" {
		r.Table, r.RenamedFrom, table = to, table, to
	}
	if ctx.Deleted[table] {
		r.DeletedOnBranch = true
		return r
	}
	r.FilePath = ctx.ResolveFile(table)
	if ctx.Changed[table] {
		r.ChangedOnBranch = true
		if sql, ok := ctx.SQLOf(table); ok {
			if Mentions(sql, name) {
				r.StillNamed = true
			} else {
				r.ProbablyUpdated = true
			}
		}
	}
	return r
}

// ImpactReaders is the readers of table as the report shows them: dev runs
// folded into their action, the table itself left out, and assertions left
// out unless this branch deletes them.
func ImpactReaders(links []lineage.ColumnLink, table, name string, ctx ReaderContext) []Reader {
	var readers []Reader
	for _, l := range links {
		r := AnnotateReader(l, name, ctx)
		assertion := l.Assertion || ctx.IsAssertion != nil && ctx.IsAssertion(r.Table)
		// An incremental table reads itself, under its old name too when
		// renamed.
		if r.Table == table || r.RenamedFrom == table || assertion && !r.DeletedOnBranch {
			continue
		}
		readers = append(readers, r)
	}
	return RankReaders(readers)
}

// IsSettled says whether a reader needs no looking at: changed on this
// branch so it probably no longer names what was dropped, or deleted on it.
func IsSettled(r Reader) bool { return r.ProbablyUpdated || r.DeletedOnBranch }

var (
	collatorMu sync.Mutex
	collator   = collate.New(language.English)
)

// localeCompare orders names as JavaScript's localeCompare does, which the
// extension sorts with: by Unicode collation, so r1 comes before R2 and
// a_b before aB, where a byte comparison says otherwise.
func localeCompare(a, b string) int {
	collatorMu.Lock()
	defer collatorMu.Unlock()
	return collator.CompareString(a, b)
}

var linkOrder = map[lineage.DependencyType]int{lineage.ExactCopy: 0, lineage.Other: 1, lineage.TableOnly: 2}

func readerRank(r Reader) int {
	switch {
	case r.DeletedOnBranch:
		return 4
	case r.FilePath == "":
		return 0 // outside this project: can't be fixed in this change
	case r.ProbablyUpdated:
		return 3
	case r.ChangedOnBranch:
		return 2
	}
	return 1
}

// RankReaders keeps one entry per reader table and column: readers outside
// the project first, then those probably updated, then those deleted.
func RankReaders(readers []Reader) []Reader {
	seen := map[string]bool{}
	var out []Reader
	for _, r := range readers {
		key := r.Table + "#" + r.Column
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Reader) int {
		return cmp.Or(
			cmp.Compare(readerRank(a), readerRank(b)),
			cmp.Compare(linkOrder[a.DependencyType], linkOrder[b.DependencyType]),
			localeCompare(a.Table, b.Table),
			localeCompare(a.Column, b.Column))
	})
	return out
}

// OpenReaders counts the readers still to look at: all but the settled ones.
func OpenReaders(readers []Reader) int {
	n := 0
	for _, r := range readers {
		if !IsSettled(r) {
			n++
		}
	}
	return n
}

func tableWeight(t Table) int {
	n := OpenReaders(t.Readers)
	for _, c := range t.Columns {
		if !c.Change.OnBase {
			n += OpenReaders(c.Readers)
		}
	}
	return n
}

func droppedFirst(c ColumnChange) int {
	if c.Kind == Dropped {
		return 0
	}
	return 1
}

// SortColumns puts the columns with the most readers still to look at
// first, drops ahead of retypes on a tie, and the base branch's changes
// after the branch's own.
func SortColumns(columns []Column) []Column {
	onBase := func(c Column) int {
		if c.Change.OnBase {
			return 1
		}
		return 0
	}
	out := slices.Clone(columns)
	slices.SortStableFunc(out, func(a, b Column) int {
		return cmp.Or(
			cmp.Compare(onBase(a), onBase(b)),
			cmp.Compare(OpenReaders(b.Readers), OpenReaders(a.Readers)),
			cmp.Compare(droppedFirst(a.Change), droppedFirst(b.Change)),
			localeCompare(a.Column, b.Column))
	})
	return out
}

// SortTables puts the tables with the most readers still to look at first.
func SortTables(tables []Table) []Table {
	out := slices.Clone(tables)
	slices.SortStableFunc(out, func(a, b Table) int {
		return cmp.Or(cmp.Compare(tableWeight(b), tableWeight(a)), localeCompare(a.Table, b.Table))
	})
	return out
}

// DescribeChange is "dropped" or "INT64 → STRING".
func DescribeChange(c ColumnChange) string {
	if c.Kind == Dropped {
		return "dropped"
	}
	return c.From + " → " + c.To
}

// ShortTable is the dataset.table of a project.dataset.table.
func ShortTable(table string) string {
	parts := strings.Split(table, ".")
	return strings.Join(parts[min(1, len(parts)-1):], ".")
}

// OtherProject is the project of reader when it isn't of's, e.g. a dev
// run, so it can't pass for a reader in prod; "" otherwise.
func OtherProject(reader, of string) string {
	project, _, _ := strings.Cut(reader, ".")
	if ofProject, _, _ := strings.Cut(of, "."); project == ofProject {
		return ""
	}
	return project
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Severity is how urgent a change is.
type Severity int

// Severities, least urgent first.
const (
	SeverityLow Severity = iota
	SeverityUnknown
	SeverityWarning
	SeverityCritical
)

// deletedChange marks a deleted table where a ColumnChange is expected.
var deletedChange = ColumnChange{Kind: "deleted"}

// ImpactSeverity is low for a change the base branch already made, which
// this branch can do nothing about; else unknown when the readers couldn't
// be looked up, low when nothing still reads it (no readers, or only ones
// this branch probably updated), else critical for a drop or deletion and a
// warning for a retype.
func ImpactSeverity(change ColumnChange, readers []Reader, readersError string) Severity {
	switch {
	case change.OnBase:
		return SeverityLow
	case readersError != "":
		return SeverityUnknown
	case OpenReaders(readers) == 0:
		return SeverityLow
	case change.Kind == TypeChanged:
		return SeverityWarning
	}
	return SeverityCritical
}

// severityOrder is the extension's order of urgency, for a table's
// severity: an unknown counts above a warning.
var severityOrder = []Severity{SeverityCritical, SeverityUnknown, SeverityWarning, SeverityLow}

// TableSeverity is the most urgent severity of what the table deletes,
// drops or retypes.
func TableSeverity(t Table) Severity {
	if t.Deleted {
		return ImpactSeverity(deletedChange, t.Readers, t.ReadersError)
	}
	seen := map[Severity]bool{}
	for _, c := range t.Columns {
		seen[ImpactSeverity(c.Change, c.Readers, c.ReadersError)] = true
	}
	for _, s := range severityOrder {
		if seen[s] {
			return s
		}
	}
	return SeverityLow
}

// IsLowImpact says whether nothing still reads what the table deletes,
// drops or retypes.
func IsLowImpact(t Table) bool { return TableSeverity(t) == SeverityLow }

// Worst is the most urgent severity in r, and false when nothing is at risk.
// An unknown counts below a warning.
func Worst(r *Report) (Severity, bool) {
	worst, found := SeverityLow, false
	for _, t := range r.AtRisk {
		if t.Deleted {
			worst, found = max(worst, ImpactSeverity(deletedChange, t.Readers, t.ReadersError)), true
		}
		for _, c := range t.Columns {
			worst, found = max(worst, ImpactSeverity(c.Change, c.Readers, c.ReadersError)), true
		}
	}
	return worst, found
}

var newlines = regexp.MustCompile(`\s*\n\s*`)

// cell is text for a Markdown table cell: pipes would end the cell,
// newlines the row.
func cell(text string) string {
	return newlines.ReplaceAllString(strings.ReplaceAll(text, "|", `\|`), " ")
}

// ReaderNote is what this branch does about a reader, or that it is
// outside the project.
func ReaderNote(r Reader) string {
	note := ""
	switch {
	case r.DeletedOnBranch:
		return "deleted here"
	case r.Checked == CheckedClear:
		note = "no longer reads it (dry run passes)"
	case r.Checked == CheckedReads:
		note = "still reads it (dry run fails)"
	case r.ProbablyUpdated:
		note = "probably updated"
	case r.StillNamed:
		note = "still names it"
	case r.FilePath == "":
		return "outside the project"
	case r.RenamedFrom == "" && r.ChangedOnBranch:
		return "changed here"
	case r.RenamedFrom == "":
		return "not changed here"
	}
	switch {
	case r.RenamedFrom != "" && note == "":
		return "renamed here"
	case r.RenamedFrom != "":
		return "renamed here, " + note
	case r.ChangedOnBranch && r.StillNamed:
		return "changed here, " + note
	}
	return note
}

// LinkLabels is how a reader reads the column, as the extension's panel
// labels it.
var LinkLabels = map[lineage.DependencyType]string{lineage.ExactCopy: "exact copy", lineage.Other: "derived", lineage.TableOnly: "may read"}

// ReaderName is reader as the report names it: dataset.table, or the full
// name when it is in another project than table.
func ReaderName(reader, table string) string {
	if OtherProject(reader, table) != "" {
		return reader
	}
	return ShortTable(reader)
}

// BaseName is the base branch as a report names it, e.g. origin/main.
func BaseName(c *Comparison) string {
	if c == nil || c.BaseRef == "" {
		return "the base branch"
	}
	return c.BaseRef
}

// HasOnBase reports whether r has a change the base branch already made.
func HasOnBase(r *Report) bool {
	for _, t := range r.AtRisk {
		for _, c := range t.Columns {
			if c.Change.OnBase {
				return true
			}
		}
	}
	return false
}

// OnBaseNote explains a change marked as the base branch's, as Markdown;
// "" when r has none.
func OnBaseNote(r *Report) string {
	if !HasOnBase(r) {
		return ""
	}
	base := BaseName(r.Comparison)
	return "\"Already changed on `" + base + "`\": `" + base + "` makes the change too, and the table compared with is behind it. " +
		"It reaches that table with the next release whatever this branch does, so it counts as low here."
}

// UnsetHint is what a report compared with the default targets says, as
// Markdown: they may be dev tables, and the setting that compares with
// prod. It is "" when the setting is set.
func UnsetHint(r *Report) string {
	if r.Unset == "" {
		return ""
	}
	targets := "the project's default targets"
	if r.Unset == "dbtProdTarget" {
		targets = "the default target of your dbt profile"
	}
	if len(r.Against) > 0 {
		targets += " (" + codeList(r.Against) + ")"
	}
	return "Compared with " + targets + ", which may be dev tables. Set `" + r.Unset + "` to compare with prod."
}

// codeList is "`a`, `b`".
func codeList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "`" + s + "`"
	}
	return strings.Join(quoted, ", ")
}

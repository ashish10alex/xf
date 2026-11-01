package frontend

import (
	"cmp"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// ActionState words an action's state for its cell: "✓ succeeded".
func ActionState(state string) string {
	glyph := "·"
	switch state {
	case "SUCCEEDED":
		glyph = "✓"
	case "FAILED", "CANCELLED":
		glyph = "✗"
	case "RUNNING":
		glyph = "▶"
	case "SKIPPED", "DISABLED":
		glyph = "–"
	}
	return glyph + " " + strings.ToLower(state)
}

// AloneMessage is what ] and [ say when the Actions pane lists a alone:
// that the file has no other actions, or for an external action in a file
// that declares others (see project.Graph.Siblings) how to find them.
func AloneMessage(g *project.Graph, a *project.Action) string {
	if g != nil && a != nil && a.Kind.External() && a.FileName != "" && len(g.ActionsInFile(a.FileName)) > 1 {
		kind := a.Kind.String()
		return kind + "s are shown alone: / $" + strings.ReplaceAll(kind, " ", "") + " finds the others"
	}
	return "no other actions in this file"
}

// AssertionKind is what a generated assertion checks, from its name:
// "…_assertions_uniqueKey_0" is uniqueKey.
func AssertionKind(id project.ActionID) string {
	_, name := SplitID(id)
	if i := strings.LastIndex(name, "_assertions_"); i >= 0 {
		name = name[i+len("_assertions_"):]
	}
	if i := strings.LastIndexByte(name, '_'); i > 0 && strings.Trim(name[i+1:], "0123456789") == "" {
		name = name[:i] // uniqueKey_0, uniqueKey_1: one per uniqueKeys entry
	}
	return name
}

// CountRun is countChanged(n), or "5 of 16 changed actions" when some of
// the total are unchecked.
func CountRun(n, total int) string {
	if n == total {
		return CountChanged(n)
	}
	return fmt.Sprintf("%d of %d changed actions", n, total)
}

// CountChanged is "1 changed action" or "5 changed actions".
func CountChanged(n int) string {
	return fmt.Sprintf("%d changed action%s", n, Plural(n))
}

func Plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func FirstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// FormatBytes uses binary units, as BigQuery bills: "0 B", "512 B", "1.20 GiB".
func FormatBytes(n int64) string { return bigquery.FormatBytes(n) }

// FormatCost is an estimated cost, to the hundredth of a cent the extension
// shows.
func FormatCost(usd float64) string { return fmt.Sprintf("$%.4f", usd) }

// FormatDuration is a duration short enough for a column, as the extension
// writes it: "1.2s", "33s", "1m 04s", "2h 06m".
func FormatDuration(d time.Duration) string {
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", max(d, 0).Seconds())
	}
	secs := int(d.Round(time.Second).Seconds())
	switch mins := secs / 60; {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case mins < 60:
		return fmt.Sprintf("%dm %02ds", mins, secs%60)
	default:
		return fmt.Sprintf("%dh %02dm", mins/60, mins%60)
	}
}

func NoSQLMessage(a *project.Action) string {
	switch a.Kind {
	case project.KindDeclaration, project.KindSource:
		return "-- " + a.Kind.String() + ": " + string(a.ID) + " is defined outside this project"
	case project.KindSeed:
		return "-- seed: loaded from " + a.FileName
	case project.KindExposure:
		return "-- exposure: " + cmp.Or(a.Description, string(a.ID))
	case project.KindUnknown:
		return "-- " + string(a.ID) + " is not defined in this project"
	default:
		return "-- no SQL"
	}
}

// RelTime says how long before now t was, roughly: 2h ago, yesterday.
func RelTime(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	days := int(d.Hours() / 24)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case days < 1:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case days == 1:
		return "yesterday"
	case days < 14:
		return fmt.Sprintf("%dd ago", days)
	case days < 60:
		return fmt.Sprintf("%dw ago", days/7)
	case days < 365:
		return fmt.Sprintf("%dmo ago", days/30)
	}
	return fmt.Sprintf("%dy ago", days/365)
}

// ShortPath is path with the home directory written ~.
func ShortPath(home, path string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return path
}

// SplitID is the dataset and name of an action ID, "database.schema.name"
// or "schema.name".
func SplitID(id project.ActionID) (schema, name string) {
	parts := strings.Split(string(id), ".")
	if len(parts) < 2 {
		return "", string(id)
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

// RunLabel names a run of actions: the action, or "3 actions".
func RunLabel(actions []*project.Action) string {
	if len(actions) == 1 {
		return actions[0].Target.Schema + "." + actions[0].Target.Name
	}
	return fmt.Sprintf("%d actions", len(actions))
}

// TagsLabel names a run of tags: "tag daily", "tags daily, hourly", or
// "5 tags" for more than three.
func TagsLabel(tags []string) string {
	switch {
	case len(tags) == 1:
		return "tag " + tags[0]
	case len(tags) > 3:
		return fmt.Sprintf("%d tags", len(tags))
	}
	return "tags " + strings.Join(tags, ", ")
}

// CostFootnote explains the figures, as the Markdown does.
const CostFootnote = `Bytes are BigQuery dry-run estimates of each changed action's query; nothing was run. ` +
	`The total counts the figure in bold: a table's run, an incremental table's incremental run, or its full build while its table isn't built yet, whose incremental run is then sized on dev's copy. Change is against the same action at the merge-base. ` +
	`"No partition filter": the query never names the partition column of a partitioned table it reads.`

// InfoTimeout bounds the tool's --version, the git commands and the
// lookups of the Google accounts.
const InfoTimeout = 30 * time.Second

// RenamedReason stands for new on a renamed action.
const RenamedReason = "renamed"

// flattenFields appends fields to out by name, each RECORD followed by the
// fields in it, named by their path from the column ("items.sku"), which is
// what a query names them by once the column is unnested.
func flattenFields(out []bigquery.Field, prefix string, fields []bigquery.Field) []bigquery.Field {
	fields = slices.Clone(fields)
	slices.SortStableFunc(fields, func(x, y bigquery.Field) int {
		return cmp.Or(cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), cmp.Compare(x.Name, y.Name))
	})
	for _, f := range fields {
		if f.Name == "" {
			continue
		}
		nested := f.Fields
		f.Name, f.Fields = prefix+f.Name, nil
		out = append(out, f)
		out = flattenFields(out, f.Name+".", nested)
	}
	return out
}

// FormatDryRun is a dry run's stat, as the extension words it: "1.20 GiB
// $0.007", "≤ 1.20 GiB $0.007" for an upper bound. warn is set when there is
// no real estimate: BigQuery reports 0 bytes when it cannot tell, and the
// query still scans data when it runs.
func FormatDryRun(d *bigquery.DryRun) (text string, warn bool) {
	switch {
	case d.Accuracy == bigquery.AccuracyUnknown:
		return "⚠ bytes unknown", true
	case d.StatementType == "SCRIPT" && d.Accuracy != bigquery.AccuracyPrecise && d.Accuracy != bigquery.AccuracyUpperBound:
		return "⚠ no estimate for script", true
	}
	var prefix string
	switch d.Accuracy {
	case bigquery.AccuracyUpperBound:
		prefix = "≤ "
	case bigquery.AccuracyLowerBound:
		prefix = "≥ "
	}
	return fmt.Sprintf("%s%s $%.3f", prefix, FormatBytes(d.Bytes), d.CostUSD()), false
}

// typeNames are the action types as the summary counts them, in its
// order: singular and plural. Dataform's are its compile output's, dbt's
// the action kinds.
var typeNames = []struct{ typ, one, many string }{
	{"table", "table", "tables"},
	{"view", "view", "views"},
	{"incremental", "incremental table", "incremental tables"},
	{"materialized view", "materialized view", "materialized views"},
	{"seed", "seed", "seeds"},
	{"snapshot", "snapshot", "snapshots"},
	{"operations", "operation", "operations"},
	{"assertion", "assertion", "assertions"},
	{"test", "test", "tests"},
	{"notebook", "notebook", "notebooks"},
}

// ChangesSummary is "16 actions in 8 files: 7 tables, 9 assertions · 16
// new", then how many were removed, if any.
func ChangesSummary(res *changes.Result) string {
	files := map[string]bool{}
	types := map[string]int{}
	reasons := map[changes.Reason]int{}
	renamed := 0
	for _, c := range res.Changed {
		files[c.FileName] = true
		types[c.Type]++
		if c.RenamedFrom != "" {
			renamed++
		}
		for _, r := range c.Reasons {
			if r == changes.ReasonNew && c.RenamedFrom != "" {
				continue
			}
			reasons[r]++
		}
	}
	count := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	var kinds []string
	for _, t := range typeNames {
		if n := types[t.typ]; n > 0 {
			kinds = append(kinds, count(n, t.one, t.many))
			delete(types, t.typ)
		}
	}
	other := 0
	for _, n := range types { // a type this list doesn't know
		other += n
	}
	if other > 0 {
		kinds = append(kinds, count(other, "other", "others"))
	}
	var why []string
	for _, r := range changes.Reasons {
		if n := reasons[r]; n > 0 {
			why = append(why, fmt.Sprintf("%d %s", n, r))
		}
		if r == changes.ReasonNew && renamed > 0 {
			why = append(why, fmt.Sprintf("%d %s", renamed, RenamedReason))
		}
	}
	summary := count(len(res.Changed), "action", "actions") + " in " + count(len(files), "file", "files") + ": " +
		strings.Join(kinds, ", ") + " · " + strings.Join(why, ", ")
	if n := len(Removed(res)); n > 0 {
		summary += fmt.Sprintf(" · %d removed", n)
	}
	return summary
}

// Removed are res's deleted actions that weren't renamed.
func Removed(res *changes.Result) []changes.Deleted {
	var out []changes.Deleted
	for _, d := range res.Deleted {
		if d.RenamedTo == "" {
			out = append(out, d)
		}
	}
	return out
}

// RenamedFrom is "← old.sqlx (95%)" for a file git calls renamed, by the
// files of res's renamed actions: the old file's name, or its path when
// it moved to another folder.
func RenamedFrom(res *changes.Result) map[string]string {
	out := map[string]string{}
	for _, c := range res.Changed {
		if c.RenamedFrom == "" {
			continue
		}
		from := c.RenamedFromFile
		if path.Dir(from) == path.Dir(c.FileName) {
			from = path.Base(from)
		}
		out[c.FileName] = fmt.Sprintf("← %s (%d%%)", from, c.Similarity)
	}
	return out
}

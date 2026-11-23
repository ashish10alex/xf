package check

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// Level is how loud a note on a changed line is.
type Level string

const (
	Error   Level = "error"
	Warning Level = "warning"
	Notice  Level = "notice"
)

// Note is a finding on a line of a file, for GitHub's annotations and
// GitLab's Code Quality report.
type Note struct {
	Level Level
	// File is the path from the repository's root; "" for a note on the
	// whole check.
	File  string
	Line  int
	Title string
	Text  string
	// Check names the kind of finding, for Code Quality.
	Check string
}

// Source is what the notes read of the files: their text as the branch has
// them, and their diff against the merge-base. Paths are the project's.
type Source interface {
	// Text is the file as checked, "" when it can't be read.
	Text(file string) string
	// Diff is the file's diff against the merge-base with no context
	// lines (git diff -U0), "" when there is none.
	Diff(file string) string
}

// Notes are the report's findings placed on lines: a column change that
// something still reads (or whose readers couldn't be looked up), a read
// without a partition filter, an action not estimated, and a gate that
// tripped. prefix is the project's folder from the repository's root.
// Levels follow the gates: error only for what a set gate trips.
func Notes(r *Report, o Options, src Source, prefix string) []Note {
	var notes []Note
	full := func(file string) string {
		if prefix == "" || prefix == "." {
			return file
		}
		return path.Join(prefix, file)
	}
	level := func(s impact.Severity) Level {
		switch {
		case o.Gate && s >= o.FailOn && s > impact.SeverityLow:
			return Error
		case s >= impact.SeverityWarning:
			return Warning
		}
		return Notice
	}
	if r.Impact != nil {
		for _, t := range r.Impact.AtRisk {
			if t.FileName == "" {
				continue
			}
			if t.Deleted {
				s := impact.TableSeverity(t)
				if s == impact.SeverityLow {
					continue
				}
				notes = append(notes, Note{Level: level(s), File: full(t.FileName), Line: 1, Check: "xf/deleted-table",
					Title: "Deleted table still read", Text: readersText("This branch deletes "+impact.ShortTable(t.Table), t.Readers, t.ReadersError)})
			}
			for _, c := range t.Columns {
				s := impact.ImpactSeverity(c.Change, c.Readers, c.ReadersError)
				if s == impact.SeverityLow {
					continue
				}
				n := Note{Level: level(s), File: full(t.FileName), Check: "xf/" + c.Change.Kind}
				switch {
				case c.Change.BreaksRun == impact.BreaksRunUniqueKey:
					keyField := "uniqueKey"
					if t.Dbt {
						keyField = "unique_key"
					}
					n.Title = "Dropped column still in " + keyField
					n.Line = namingLine(src.Text(t.FileName), c.Column) // the config's uniqueKey, ahead of the query
					n.Text = fmt.Sprintf("This branch drops %s from %s, but its %s still names it, so every incremental run fails, after a full refresh too: take %s out of %s.", c.Column, impact.ShortTable(t.Table), keyField, c.Column, keyField) + alsoRead(c.Readers)
				case c.Change.BreaksRun == impact.BreaksRunTable:
					n.Title = "Dropped column breaks the incremental run"
					n.Line = removedLine(src.Diff(t.FileName), c.Column)
					if t.Dbt {
						if c.Change.Kind == impact.TypeChanged {
							n.Title = "Retyped column breaks the incremental run"
							n.Line = namingLine(src.Text(t.FileName), c.Column)
							n.Text = fmt.Sprintf("This branch changes %s of %s from %s to %s, which fails dbt's incremental run with on_schema_change: fail until --full-refresh rebuilds it.", c.Column, impact.ShortTable(t.Table), c.Change.From, c.Change.To) + alsoRead(c.Readers)
						} else {
							n.Text = fmt.Sprintf("This branch drops %s from %s, whose target table still has it: dbt's incremental run fails until --full-refresh rebuilds it.", c.Column, impact.ShortTable(t.Table)) + alsoRead(c.Readers)
						}
					} else if c.Change.Kind == impact.TypeChanged {
						n.Title = "Retyped column breaks the incremental run"
						n.Line = namingLine(src.Text(t.FileName), c.Column)
						n.Text = fmt.Sprintf("This branch changes %s of %s from %s to %s, which fails Dataform's incremental run with onSchemaChange: FAIL until a full refresh rebuilds it.", c.Column, impact.ShortTable(t.Table), c.Change.From, c.Change.To) + alsoRead(c.Readers)
					} else {
						n.Text = fmt.Sprintf("This branch drops %s from %s, whose table in BigQuery still has it: Dataform's incremental MERGE or INSERT names every column of the table, so the next incremental run fails until a full refresh rebuilds it.", c.Column, impact.ShortTable(t.Table)) + alsoRead(c.Readers)
					}
				case c.Change.Kind == impact.Dropped:
					n.Title = "Dropped column still read"
					n.Line = removedLine(src.Diff(t.FileName), c.Column)
					n.Text = readersText(fmt.Sprintf("This branch drops %s from %s", c.Column, impact.ShortTable(t.Table)), c.Readers, c.ReadersError)
				default:
					n.Title = "Retyped column still read"
					n.Line = namingLine(src.Text(t.FileName), c.Column)
					n.Text = readersText(fmt.Sprintf("This branch changes %s of %s from %s to %s", c.Column, impact.ShortTable(t.Table), c.Change.From, c.Change.To), c.Readers, c.ReadersError)
				}
				if s == impact.SeverityUnknown {
					n.Title = "Readers couldn't be looked up"
				}
				notes = append(notes, n)
			}
		}
		if o.FailOnUnchecked {
			for _, u := range impact.UncheckedFailures(r.Impact) {
				file := ""
				if u.FileName != "" {
					file = full(u.FileName)
				}
				notes = append(notes, Note{Level: Error, File: file, Line: 1, Check: "xf/unchecked",
					Title: "Model not checked",
					Text:  fmt.Sprintf("%s could not be checked: %s", impact.ShortTable(u.Table), oneLine(u.Reason))})
			}
		}
	}
	if r.Cost != nil {
		unfilteredLevel := Warning
		if o.Cost.FailOnUnfiltered {
			unfilteredLevel = Error
		}
		unestimatedLevel := Notice
		if o.Cost.FailOnUnestimated {
			unestimatedLevel = Error
		}
		deltaTripped := r.Cost.ExceedsDelta(o.Cost.DeltaThreshold)
		noBase := r.Cost.BaseError != "" || r.Cost.BasePending
		type deltaHit struct {
			action cost.Action
			delta  int64
		}
		var deltaHits []deltaHit
		for _, a := range r.Cost.Actions {
			if a.FileName == "" {
				continue
			}
			for _, u := range a.Unfiltered {
				table := impact.ShortTable(u.Table)
				title := "No partition filter"
				text := fmt.Sprintf("This query reads %s, partitioned by %s, without filtering on %s, so no partition can be pruned.", table, u.Column, u.Column)
				if u.MergeTarget {
					title = "MERGE target without partition filter"
					text = fmt.Sprintf("This MERGE writes to %s, partitioned by %s, without filtering on %s in its ON or WHEN clauses, so BigQuery scans the full target table.", table, u.Column, u.Column)
				}
				notes = append(notes, Note{Level: unfilteredLevel, File: full(a.FileName), Line: namingLine(src.Text(a.FileName), lastPart(u.Table)),
					Check: "xf/missing-partition-filter", Title: title, Text: text})
			}
			if a.Estimate == nil {
				notes = append(notes, Note{Level: unestimatedLevel, File: full(a.FileName), Line: 1, Check: "xf/not-estimated",
					Title: "Not estimated", Text: "BigQuery gave no estimate for " + impact.ShortTable(a.Name) + ": " + oneLine(a.WhyNot())})
			} else if deltaTripped && (a.Base == nil || a.Estimate.Bytes > a.Base.Bytes) {
				delta := a.Estimate.Bytes
				if a.Base != nil {
					delta -= a.Base.Bytes
				}
				if delta > 0 {
					deltaHits = append(deltaHits, deltaHit{action: a, delta: delta})
				}
			}
		}
		if r.Cost.PastRuns != nil {
			for _, run := range r.Cost.PastRuns.Runs {
				if run.FileName == "" {
					continue
				}
				for _, u := range run.Unfiltered {
					table := impact.ShortTable(u.Table)
					notes = append(notes, Note{Level: unfilteredLevel, File: full(run.FileName), Line: namingLine(src.Text(run.FileName), lastPart(u.Table)),
						Check: "xf/missing-partition-filter", Title: "No partition/cluster filter",
						Text: fmt.Sprintf("This query reads %s, partitioned/clustered by %s, without filtering on %s.", table, u.Column, u.Column)})
				}
			}
		}
		if deltaTripped && len(deltaHits) > 0 {
			slices.SortStableFunc(deltaHits, func(a, b deltaHit) int {
				return cmp.Or(cmp.Compare(b.delta, a.delta), cmp.Compare(a.action.Name, b.action.Name))
			})
			minDelta := cmp.Or(max(o.QuietUnder, 0), DefaultQuietUnder)
			var selected []deltaHit
			for _, h := range deltaHits {
				if h.delta >= minDelta {
					selected = append(selected, h)
				}
			}
			if len(selected) == 0 {
				selected = deltaHits[:1]
			}
			if len(selected) > 5 {
				selected = selected[:5]
			}
			for i, h := range selected {
				a := h.action
				lvl := Error
				if i > 0 && h.delta < o.Cost.DeltaThreshold/2 {
					lvl = Warning
				}
				var text string
				switch {
				case noBase:
					text = fmt.Sprintf("%s scans %s (%s) per run (no merge-base to compare with), counted toward the %s delta limit.", impact.ShortTable(a.Name), warehouse.FormatBytes(h.delta), cost.FormatUSD(o.Cost.USD(h.delta)), warehouse.FormatBytes(o.Cost.DeltaThreshold))
				case a.Base == nil && !a.New:
					text = fmt.Sprintf("%s scans %s (%s) per run (base estimate unavailable, counted as growth), contributing to the %s delta limit.", impact.ShortTable(a.Name), warehouse.FormatBytes(h.delta), cost.FormatUSD(o.Cost.USD(h.delta)), warehouse.FormatBytes(o.Cost.DeltaThreshold))
				default:
					text = fmt.Sprintf("%s adds +%s (%s) per run, pushing the branch over the %s delta limit.", impact.ShortTable(a.Name), warehouse.FormatBytes(h.delta), cost.SignedUSD(o.Cost.USD(h.delta)), warehouse.FormatBytes(o.Cost.DeltaThreshold))
				}
				notes = append(notes, Note{Level: lvl, File: full(a.FileName), Line: 1, Check: "xf/cost-delta",
					Title: "Increases query scan cost", Text: text})
			}
		}
		if r.Cost.Exceeds(o.Cost.Threshold) {
			total, bound := r.Cost.Total()
			notes = append(notes, Note{Level: Error, Check: "xf/cost-limit", Title: "Over the cost limit",
				Text: fmt.Sprintf("The changed actions scan %s%s between them, over the limit of %s.", bound, warehouse.FormatBytes(total), warehouse.FormatBytes(o.Cost.Threshold))})
		}
		if deltaTripped {
			text := fmt.Sprintf("The changed actions add %s of scan per run, over the delta limit of %s.", warehouse.FormatBytes(r.Cost.Growth()), warehouse.FormatBytes(o.Cost.DeltaThreshold))
			if noBase {
				text = fmt.Sprintf("No merge-base estimate; the changed actions scan %s per run, counted as growth over the delta limit of %s.", warehouse.FormatBytes(r.Cost.Growth()), warehouse.FormatBytes(o.Cost.DeltaThreshold))
			}
			notes = append(notes, Note{Level: Error, Check: "xf/cost-delta-limit", Title: "Over the cost growth limit", Text: text})
		}
	}
	return notes
}

// readersText is what a change is, and who still reads it.
func readersText(what string, readers []impact.Reader, readersError string) string {
	if readersError != "" {
		return what + "; its readers couldn't be looked up: " + oneLine(readersError)
	}
	names := readerNames(readers)
	if len(names) == 0 {
		return what + "."
	}
	return what + ", which " + strings.Join(names, ", ") + map[bool]string{true: " reads.", false: " read."}[len(names) == 1]
}

// alsoRead is a sentence naming the readers still to fix, after one that
// says what breaks; "" when there are none.
func alsoRead(readers []impact.Reader) string {
	names := readerNames(readers)
	if len(names) == 0 {
		return ""
	}
	return " " + strings.Join(names, ", ") + map[bool]string{true: " reads it too.", false: " read it too."}[len(names) == 1]
}

// readerNames is the readers still to fix, five at most and then how many
// more.
func readerNames(readers []impact.Reader) []string {
	var names []string
	for _, rd := range impact.RankReaders(readers) {
		if impact.IsSettled(rd) {
			continue
		}
		name := impact.ReaderDisplay(rd)
		if rd.FilePath != "" {
			name += " (" + rd.FilePath + ")"
		}
		names = append(names, name)
	}
	if len(names) > 5 {
		names = append(names[:5], fmt.Sprintf("and %d more", len(names)-5))
	}
	return names
}

func lastPart(table string) string {
	return table[strings.LastIndex(table, ".")+1:]
}

// word matches name as a whole identifier, any case.
func word(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_])`)
}

// namingLine is the first line of text that names name, else 1. For a
// dotted nested field ("user.zip"), it falls back to the leaf ("zip").
func namingLine(text, name string) int {
	if line, ok := findNamingLine(text, name); ok {
		return line
	}
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		if line, ok := findNamingLine(text, name[dot+1:]); ok {
			return line
		}
	}
	return 1
}

func findNamingLine(text, name string) (int, bool) {
	re := word(name)
	for i, line := range strings.Split(text, "\n") {
		if re.MatchString(line) {
			return i + 1, true
		}
	}
	return 1, false
}

var hunkRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// removedLine is the first line, in the new file, of the diff hunk whose
// removed lines name column, else 1. A hunk that only removes lines goes
// on the line before them. For a dotted nested field ("user.zip"), it
// falls back to the leaf ("zip").
func removedLine(diff, column string) int {
	if line, ok := findRemovedLine(diff, column); ok {
		return line
	}
	if dot := strings.LastIndexByte(column, '.'); dot >= 0 {
		if line, ok := findRemovedLine(diff, column[dot+1:]); ok {
			return line
		}
	}
	return 1
}

func findRemovedLine(diff, column string) (int, bool) {
	re := word(column)
	start := 0
	for _, line := range strings.Split(diff, "\n") {
		if m := hunkRE.FindStringSubmatch(line); m != nil {
			start, _ = strconv.Atoi(m[1])
			continue
		}
		if start > 0 && strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") && re.MatchString(line[1:]) {
			return max(start, 1), true
		}
	}
	return 1, false
}

// GitHub is the notes as GitHub Actions' workflow commands, a line each.
func GitHub(notes []Note) string {
	var b strings.Builder
	for _, n := range notes {
		var props []string
		if n.File != "" {
			props = append(props, "file="+escapeProp(n.File), "line="+strconv.Itoa(max(n.Line, 1)))
		}
		if n.Title != "" {
			props = append(props, "title="+escapeProp(n.Title))
		}
		b.WriteString("::" + string(n.Level))
		if len(props) > 0 {
			b.WriteString(" " + strings.Join(props, ","))
		}
		b.WriteString("::" + escapeData(n.Text) + "\n")
	}
	return b.String()
}

func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProp(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

// codeQualitySeverity maps a note's level to Code Quality's.
var codeQualitySeverity = map[Level]string{Error: "critical", Warning: "major", Notice: "info"}

// CodeQuality is the notes as a GitLab Code Quality report. A note on the
// whole check has no file to sit on, and is left out.
func CodeQuality(notes []Note) ([]byte, error) {
	type lines struct {
		Begin int `json:"begin"`
	}
	type location struct {
		Path  string `json:"path"`
		Lines lines  `json:"lines"`
	}
	type issue struct {
		Description string   `json:"description"`
		CheckName   string   `json:"check_name"`
		Fingerprint string   `json:"fingerprint"`
		Severity    string   `json:"severity"`
		Location    location `json:"location"`
	}
	out := []issue{}
	for _, n := range notes {
		if n.File == "" {
			continue
		}
		sum := sha256.Sum256([]byte(n.Check + "\x00" + n.File + "\x00" + n.Text))
		out = append(out, issue{Description: n.Title + ": " + n.Text, CheckName: n.Check, Fingerprint: hex.EncodeToString(sum[:16]),
			Severity: codeQualitySeverity[n.Level], Location: location{Path: n.File, Lines: lines{Begin: max(n.Line, 1)}}})
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

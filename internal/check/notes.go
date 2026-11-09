package check

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/impact"
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
		case o.Gate && s >= o.FailOn && s >= impact.SeverityWarning:
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
				if c.Change.Kind == impact.Dropped {
					n.Title = "Dropped column still read"
					n.Line = removedLine(src.Diff(t.FileName), c.Column)
					n.Text = readersText(fmt.Sprintf("This branch drops %s from %s", c.Column, impact.ShortTable(t.Table)), c.Readers, c.ReadersError)
				} else {
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
	}
	if r.Cost != nil {
		for _, a := range r.Cost.Actions {
			if a.FileName == "" {
				continue
			}
			for _, u := range a.Unfiltered {
				table := impact.ShortTable(u.Table)
				notes = append(notes, Note{Level: Warning, File: full(a.FileName), Line: namingLine(src.Text(a.FileName), lastPart(u.Table)),
					Check: "xf/missing-partition-filter", Title: "No partition filter",
					Text: fmt.Sprintf("This query reads %s, partitioned by %s, without naming %s, so no partition can be pruned.", table, u.Column, u.Column)})
			}
			if a.Estimate == nil {
				notes = append(notes, Note{Level: Notice, File: full(a.FileName), Line: 1, Check: "xf/not-estimated",
					Title: "Not estimated", Text: "BigQuery gave no estimate for " + impact.ShortTable(a.Name) + ": " + oneLine(a.Reason)})
			}
		}
		if r.CostTripped(o) {
			total, bound := r.Cost.Total()
			notes = append(notes, Note{Level: Error, Check: "xf/cost-limit", Title: "Over the cost limit",
				Text: fmt.Sprintf("The changed actions scan %s%s between them, over the limit of %s.", bound, bigquery.FormatBytes(total), bigquery.FormatBytes(o.Cost.Threshold))})
		}
	}
	return notes
}

// readersText is what a change is, and who still reads it.
func readersText(what string, readers []impact.Reader, readersError string) string {
	if readersError != "" {
		return what + "; its readers couldn't be looked up: " + oneLine(readersError)
	}
	var names []string
	for _, rd := range impact.RankReaders(readers) {
		if impact.IsSettled(rd) {
			continue
		}
		name := impact.ShortTable(rd.Table)
		if rd.FilePath != "" {
			name += " (" + rd.FilePath + ")"
		}
		names = append(names, name)
	}
	switch {
	case len(names) == 0:
		return what + "."
	case len(names) > 5:
		names = append(names[:5], fmt.Sprintf("and %d more", len(names)-5))
	}
	return what + ", which " + strings.Join(names, ", ") + map[bool]string{true: " reads.", false: " read."}[len(names) == 1]
}

func lastPart(table string) string {
	return table[strings.LastIndex(table, ".")+1:]
}

// word matches name as a whole identifier, any case.
func word(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_])`)
}

// namingLine is the first line of text that names name, else 1.
func namingLine(text, name string) int {
	re := word(name)
	for i, line := range strings.Split(text, "\n") {
		if re.MatchString(line) {
			return i + 1
		}
	}
	return 1
}

var hunkRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// removedLine is the first line, in the new file, of the diff hunk whose
// removed lines name column, else 1. A hunk that only removes lines goes
// on the line before them.
func removedLine(diff, column string) int {
	re := word(column)
	start := 0
	for _, line := range strings.Split(diff, "\n") {
		if m := hunkRE.FindStringSubmatch(line); m != nil {
			start, _ = strconv.Atoi(m[1])
			continue
		}
		if start > 0 && strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") && re.MatchString(line[1:]) {
			return max(start, 1)
		}
	}
	return 1
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

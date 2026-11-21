package cost

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// Cost on a warehouse that bills its time rather than the bytes a query
// scans, Databricks, has nothing to estimate before a run (ADR 0042). Its
// report is instead each changed model's latest run with the prod target,
// from the warehouse's query history, labelled as a past run, with no
// dollar figure: Report.PastRuns, with no Actions.

// PastRuns is what the changed models' latest runs did.
type PastRuns struct {
	// Source names where the runs came from: "Databricks Query History".
	Source string `json:"source"`
	// Target is the dbt target whose runs were kept, Days how far back
	// they were looked for.
	Target string `json:"target"`
	Days   int    `json:"days"`
	// Runs are the changed models: most bytes read first, then those with
	// no run, by name.
	Runs []PastRun `json:"runs"`
	// Why says why no model has a run, when something other than their
	// not having run explains it: dbt's query comment turned off, or runs
	// seen only with other targets.
	Why string `json:"why,omitempty"`
	// Truncated says the history was read only in part.
	Truncated bool `json:"truncated,omitempty"`
	// NoReadRows is set when the warehouse's query history does not report
	// rows read (Snowflake reports bytes scanned and rows produced, not rows
	// read), so text/markdown/web omit the "Rows read" column instead of
	// showing 0.
	NoReadRows bool `json:"noReadRows,omitempty"`
	// WarehouseUsed names the warehouse queried to read history when reading
	// history itself consumes compute (Snowflake ACCOUNT_USAGE).
	WarehouseUsed string `json:"warehouseUsed,omitempty"`
}

// PastRun is a changed model and its latest run, if it has one.
type PastRun struct {
	Name     string `json:"name"`
	FileName string `json:"fileName,omitempty"`
	Type     string `json:"type"`
	// New is set on a model the merge-base doesn't have.
	New bool `json:"new,omitempty"`
	// Ran says a run was found; the rest is about it.
	Ran      bool          `json:"ran"`
	Started  time.Time     `json:"started,omitzero"`
	Duration time.Duration `json:"durationNs,omitempty"`
	// ReadBytes and ReadRows are what the run read. Query History counts
	// no rows a CREATE OR REPLACE TABLE writes.
	ReadBytes       int64         `json:"readBytes,omitempty"`
	ReadRows        int64         `json:"readRows,omitempty"`
	Warehouse       string        `json:"warehouse,omitempty"`
	WarehouseSize   string        `json:"warehouseSize,omitempty"`
	Serverless      bool          `json:"serverless,omitempty"`
	Credits         float64       `json:"credits,omitempty"`
	StatementCount  int           `json:"statementCount,omitempty"`
	RunCount        int           `json:"runCount,omitempty"`
	RunsCapped      bool          `json:"runsCapped,omitempty"`
	MedianDuration  time.Duration `json:"medianDurationNs,omitempty"`
	MedianReadBytes int64         `json:"medianReadBytes,omitempty"`
	QueryID         string        `json:"queryId,omitempty"`
	// Unfiltered are upstream partitioned or clustered tables whose key column
	// this model's compiled SQL never names.
	Unfiltered []Unfiltered `json:"unfiltered,omitempty"`
}

// WarehouseLabel formats Warehouse with its size and serverless flag when
// known (e.g. "Prod jobs (Medium, serverless)"), or plain Warehouse otherwise.
func (r PastRun) WarehouseLabel() string {
	var tags []string
	if r.WarehouseSize != "" {
		tags = append(tags, r.WarehouseSize)
	}
	if r.Serverless {
		tags = append(tags, "serverless")
	}
	if len(tags) == 0 {
		return r.Warehouse
	}
	if r.Warehouse == "" {
		return strings.Join(tags, ", ")
	}
	return r.Warehouse + " (" + strings.Join(tags, ", ") + ")"
}

// CreditsText formats Snowflake compute credits, "" when zero.
func (r PastRun) CreditsText() string {
	if r.Credits <= 0 {
		return ""
	}
	if r.Credits >= 1 {
		return fmt.Sprintf("%.2f credits", r.Credits)
	}
	return fmt.Sprintf("%.3f credits", r.Credits)
}

// CadenceText summarizes multi-run cadence and median stats when more than one
// run was observed in the history window, "" otherwise.
func (r PastRun) CadenceText() string {
	if r.RunCount <= 1 {
		return ""
	}
	med := PastRun{Duration: r.MedianDuration}.Took()
	prefix := ""
	if r.RunsCapped {
		prefix = "≥"
	}
	return fmt.Sprintf("%s%d runs, median %s / %s", prefix, r.RunCount, med, warehouse.FormatBytes(r.MedianReadBytes))
}

// UnfilteredCount returns how many models in p have at least one unfiltered
// partition/clustering warning.
func (p *PastRuns) UnfilteredCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, r := range p.Runs {
		if len(r.Unfiltered) > 0 {
			n++
		}
	}
	return n
}

// SortPastRuns orders runs as PastRuns.Runs are.
func SortPastRuns(runs []PastRun) {
	slices.SortStableFunc(runs, func(a, b PastRun) int {
		switch {
		case a.Ran != b.Ran:
			if a.Ran {
				return -1
			}
			return 1
		case a.ReadBytes != b.ReadBytes:
			if a.ReadBytes > b.ReadBytes {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// ReadTotal is what the runs found read between them, and how many there
// are.
func (p *PastRuns) ReadTotal() (bytes int64, ran int) {
	for _, r := range p.Runs {
		if r.Ran {
			bytes += r.ReadBytes
			ran++
		}
	}
	return bytes, ran
}

// When is a run's date and time, in the local zone.
func (r PastRun) When() string { return r.Started.Local().Format("Jan 2 15:04") }

// Took is a run's duration, to the second: "2m 31s".
func (r PastRun) Took() string {
	d := r.Duration.Round(time.Second)
	switch {
	case r.Duration > 0 && d == 0:
		return "<1s"
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

// RowsText is a count of rows with thousands separated: "1,234,567".
func RowsText(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// NoRunText says why a model has no run.
func (r PastRun) NoRunText(p *PastRuns) string {
	if r.New {
		return "new on this branch: not run yet"
	}
	return fmt.Sprintf("no run in the last %d days", p.Days)
}

// PastLine is what a report of past runs is, in a sentence: "Last runs
// from Databricks Query History, target prod, in the last 30 days".
func PastLine(r *Report, code func(string) string) string {
	p := r.PastRuns
	s := "Last runs from " + p.Source
	if p.WarehouseUsed != "" {
		s += " (read on virtual warehouse " + code(p.WarehouseUsed) + ")"
	}
	if p.Target != "" {
		s += ", target " + code(p.Target)
	}
	s += fmt.Sprintf(", in the last %d days", p.Days)
	if p.Truncated {
		s += " (the history was read only in part)"
	}
	return s + ": nothing is estimated before a run, as the warehouse bills its time, not the bytes a query scans"
}

// PastTotalLine is "Last runs read 4.20 GiB between them (3 of 4 models)".
func PastTotalLine(r *Report) string {
	bytes, ran := r.PastRuns.ReadTotal()
	if ran == 0 {
		return "No changed model has a run to show"
	}
	return fmt.Sprintf("Last runs read %s between them (%d of %d models)", warehouse.FormatBytes(bytes), ran, len(r.PastRuns.Runs))
}

// pastUnsetHint is UnsetHint for past runs.
func pastUnsetHint(r *Report) string {
	if r.Unset == "" {
		return ""
	}
	return "These are runs of the default target of your dbt profile, which may be dev runs. Set `" + r.Unset + "` to show prod's."
}

func pastText(r *Report, o Options) string {
	var b strings.Builder
	p := r.PastRuns
	what := o.paint(plural(len(p.Runs), "changed model", "changed models"), ansiBold)
	if c := r.Comparison; c != nil {
		fmt.Fprintf(&b, "%s in %s\n", what, heading(c))
	} else {
		fmt.Fprintf(&b, "%s\n", what)
	}
	b.WriteString(o.paint(PastLine(r, func(s string) string { return s })+".", ansiDim) + "\n")
	if hint := pastUnsetHint(r); hint != "" {
		b.WriteString(o.paint("⚠ "+strings.ReplaceAll(hint, "`", ""), ansiYellow) + "\n")
	}
	if p.Why != "" {
		b.WriteString(o.paint("⚠ "+p.Why, ansiYellow) + "\n")
	}
	width := 0
	for _, run := range p.Runs {
		width = max(width, len(run.Name))
	}
	if len(p.Runs) > 0 {
		b.WriteString("\n")
	}
	for _, run := range p.Runs {
		name := run.Name + strings.Repeat(" ", width-len(run.Name))
		var extra []string
		if cr := run.CreditsText(); cr != "" {
			extra = append(extra, cr)
		}
		if cad := run.CadenceText(); cad != "" {
			extra = append(extra, cad)
		}
		if run.StatementCount > 1 {
			extra = append(extra, fmt.Sprintf("%d statements", run.StatementCount))
		}
		suffix := ""
		if len(extra) > 0 {
			suffix = " · " + strings.Join(extra, " · ")
		}
		var warnParts []string
		for _, u := range run.Unfiltered {
			warnParts = append(warnParts, o.paint(fmt.Sprintf("[MISSING PARTITION/CLUSTER FILTER: %s on %s]", u.Column, impact.ShortTable(u.Table)), ansiYellow))
		}
		warnSuffix := ""
		if len(warnParts) > 0 {
			warnSuffix = "  " + strings.Join(warnParts, "  ")
		}
		if !run.Ran {
			fmt.Fprintf(&b, "  %s  %s%s\n", name, o.paint(run.NoRunText(p), ansiDim), warnSuffix)
			continue
		}
		if p.NoReadRows {
			fmt.Fprintf(&b, "  %s  ran %s · %s · read %s · on %s%s%s\n", name, run.When(), run.Took(),
				warehouse.FormatBytes(run.ReadBytes), run.WarehouseLabel(), suffix, warnSuffix)
			continue
		}
		fmt.Fprintf(&b, "  %s  ran %s · %s · read %s, %s rows · on %s%s%s\n", name, run.When(), run.Took(),
			warehouse.FormatBytes(run.ReadBytes), RowsText(run.ReadRows), run.WarehouseLabel(), suffix, warnSuffix)
	}
	if len(p.Runs) == 0 {
		b.WriteString(o.paint("Nothing to show: the branch changes no model.", ansiDim) + "\n")
	} else {
		b.WriteString("\n" + o.paint(PastTotalLine(r), ansiBold) + "\n")
	}
	if line := GateLine(r, o); line != "" {
		b.WriteString(line + "\n")
	}
	return b.String()
}

func pastMarkdown(r *Report, o Options) string {
	p := r.PastRuns
	lines := []string{"### Query Cost of Branch Changes", ""}
	if c := r.Comparison; c != nil {
		lines = append(lines, fmt.Sprintf("**Branch:** `%s` vs `%s` @ `%s` | %s", c.HeadRef, c.BaseRef, c.MergeBase[:min(len(c.MergeBase), 7)], c.HeadLabel), "")
	}
	lines = append(lines, PastLine(r, func(s string) string { return "`" + s + "`" })+".", "")
	if hint := pastUnsetHint(r); hint != "" {
		lines = append(lines, "⚠️ "+hint, "")
	}
	if p.Why != "" {
		lines = append(lines, "⚠️ "+p.Why, "")
	}
	if len(p.Runs) == 0 {
		lines = append(lines, "Nothing to show: the branch changes no model.")
		return strings.Join(lines, "\n") + "\n"
	}
	whCell := func(run PastRun) string {
		parts := []string{}
		if wh := run.WarehouseLabel(); wh != "" {
			parts = append(parts, cell(wh))
		}
		if cr := run.CreditsText(); cr != "" {
			parts = append(parts, cr)
		}
		if cad := run.CadenceText(); cad != "" {
			parts = append(parts, cad)
		}
		for _, u := range run.Unfiltered {
			parts = append(parts, fmt.Sprintf("⚠️ no partition/cluster filter: `%s` of `%s`", cell(u.Column), cell(impact.ShortTable(u.Table))))
		}
		return strings.Join(parts, " · ")
	}
	noRunCell := func(run PastRun) string {
		parts := []string{run.NoRunText(p)}
		for _, u := range run.Unfiltered {
			parts = append(parts, fmt.Sprintf("⚠️ no partition/cluster filter: `%s` of `%s`", cell(u.Column), cell(impact.ShortTable(u.Table))))
		}
		return strings.Join(parts, " · ")
	}
	if p.NoReadRows {
		lines = append(lines, "**"+PastTotalLine(r)+"**", "",
			"| Model | Type | Last run | Took | Read | Warehouse |", "| --- | --- | --- | --: | --: | --- |")
		for _, run := range p.Runs {
			if !run.Ran {
				lines = append(lines, fmt.Sprintf("| `%s` | %s | %s | | | |", run.Name, run.Type, noRunCell(run)))
				continue
			}
			lines = append(lines, fmt.Sprintf("| `%s` | %s | %s | %s | %s | %s |", run.Name, run.Type, run.When(), run.Took(),
				warehouse.FormatBytes(run.ReadBytes), whCell(run)))
		}
	} else {
		lines = append(lines, "**"+PastTotalLine(r)+"**", "",
			"| Model | Type | Last run | Took | Read | Rows read | Warehouse |", "| --- | --- | --- | --: | --: | --: | --- |")
		for _, run := range p.Runs {
			if !run.Ran {
				lines = append(lines, fmt.Sprintf("| `%s` | %s | %s | | | | |", run.Name, run.Type, noRunCell(run)))
				continue
			}
			lines = append(lines, fmt.Sprintf("| `%s` | %s | %s | %s | %s | %s | %s |", run.Name, run.Type, run.When(), run.Took(),
				warehouse.FormatBytes(run.ReadBytes), RowsText(run.ReadRows), whCell(run)))
		}
	}
	if line := GateLine(r, o); line != "" {
		lines = append(lines, "", line)
	}
	return strings.Join(lines, "\n") + "\n"
}

// Package check is xf check (ADR 0040): column impact and query cost of a
// branch in one report, for a pull request. It says whether the report is
// worth a comment, writes the comment's body and the notes on the changed
// lines, and keeps the rules for editing a comment an earlier push left.
package check

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/impact"
)

// JSONVersion is the version of the JSON document, as impact's and cost's.
const JSONVersion = 1

// DefaultQuietUnder is the growth in a run's bytes below which cost alone
// isn't worth a comment: 1 GiB.
const DefaultQuietUnder int64 = 1 << 30

// Report is what xf check found: each check's report, or why it couldn't
// be made.
type Report struct {
	// Project is the project's folder in the repository, "." at its root.
	Project string
	Impact  *impact.Report
	// ImpactError is why the column impact couldn't be checked.
	ImpactError string
	Cost        *cost.Report
	CostError   string
}

// Options are the gates and what decides a comment.
type Options struct {
	// FailOn is the --fail-on severity; Gate says it is set.
	FailOn impact.Severity
	Gate   bool
	// Cost holds --fail-over (Threshold) and the price.
	Cost cost.Options
	// QuietUnder is the growth in bytes a run may have before cost alone
	// is worth a comment.
	QuietUnder int64
}

// Exit codes.
const (
	OK      = 0 // the report was made, and no gate tripped
	Tripped = 1 // a gate tripped
	Failed  = 2 // neither check could be made
)

// Neither says neither check could be made.
func (r *Report) Neither() bool { return r.Impact == nil && r.Cost == nil }

// ImpactTripped says --fail-on found a change that severe.
func (r *Report) ImpactTripped(o Options) bool {
	if r.Impact == nil || !o.Gate {
		return false
	}
	worst, found := impact.Worst(r.Impact)
	return found && worst >= o.FailOn
}

// CostTripped says the total is over --fail-over.
func (r *Report) CostTripped(o Options) bool {
	return r.Cost != nil && r.Cost.Exceeds(o.Cost.Threshold)
}

// Code is the exit code.
func (r *Report) Code(o Options) int {
	switch {
	case r.Neither():
		return Failed
	case r.ImpactTripped(o) || r.CostTripped(o):
		return Tripped
	}
	return OK
}

// Worth says the report is worth a comment: a check that couldn't be made,
// a gate that tripped, a column change that something still reads (or
// whose readers couldn't be looked up), a model that couldn't be compared
// with its table, a missing partition filter, an
// action not estimated, or a run that grows by QuietUnder or more (its
// total, when the merge-base wasn't estimated).
func (r *Report) Worth(o Options) bool {
	if r.ImpactError != "" || r.CostError != "" || r.ImpactTripped(o) || r.CostTripped(o) {
		return true
	}
	if r.Impact != nil {
		if worst, found := impact.Worst(r.Impact); (found && worst > impact.SeverityLow) || len(r.Impact.Unchecked) > 0 {
			return true
		}
	}
	if c := r.Cost; c != nil {
		if c.Unestimated() > 0 {
			return true
		}
		for _, a := range c.Actions {
			if len(a.Unfiltered) > 0 {
				return true
			}
		}
		total, _ := c.Total()
		growth := total
		if base, ok := c.BaseTotal(); ok {
			growth = total - base
		}
		if growth >= max(o.QuietUnder, 1) {
			return true
		}
	}
	return false
}

// Verdict is the comment's first line: the worst column change, then what
// the branch adds to the cost of a run.
func Verdict(r *Report, o Options) string {
	return impactVerdict(r) + " · " + costVerdict(r, o)
}

func impactVerdict(r *Report) string {
	if r.Impact == nil {
		return "❔ Column impact couldn't be checked"
	}
	var dropped, deleted, retyped, unknown int
	for _, t := range r.Impact.AtRisk {
		if t.Deleted {
			switch impact.TableSeverity(t) {
			case impact.SeverityCritical:
				deleted++
			case impact.SeverityUnknown:
				unknown++
			}
		}
		for _, c := range t.Columns {
			switch impact.ImpactSeverity(c.Change, c.Readers, c.ReadersError) {
			case impact.SeverityCritical:
				dropped++
			case impact.SeverityWarning:
				retyped++
			case impact.SeverityUnknown:
				unknown++
			}
		}
	}
	switch {
	case dropped > 0 && deleted > 0:
		return fmt.Sprintf("🚨 %s and %s still read", count(dropped, "dropped column"), count(deleted, "deleted table"))
	case dropped > 0:
		return "🚨 " + count(dropped, "dropped column") + " still read"
	case deleted > 0:
		return "🚨 " + count(deleted, "deleted table") + " still read"
	case retyped > 0:
		return "⚠️ " + count(retyped, "retyped column") + " still read"
	case unknown > 0 && len(r.Impact.Unchecked) > 0:
		return fmt.Sprintf("❔ the readers of %s couldn't be looked up, and %s not checked", count(unknown, "change"), count(len(r.Impact.Unchecked), "model"))
	case unknown > 0:
		return "❔ the readers of " + count(unknown, "change") + " couldn't be looked up"
	case len(r.Impact.Unchecked) > 0:
		return "❔ " + count(len(r.Impact.Unchecked), "model") + " not checked"
	}
	return "✅ no columns at risk"
}

func costVerdict(r *Report, o Options) string {
	c := r.Cost
	if c == nil {
		return "❔ query cost couldn't be checked"
	}
	if len(c.Actions) == 0 {
		return "nothing to dry run"
	}
	total, bound := c.Total()
	totalText := bound + bigquery.FormatBytes(total)
	text := ""
	if base, ok := c.BaseTotal(); ok {
		d := total - base
		switch {
		case d == 0:
			text = "no change per run · " + totalText + " total"
		case d < 0:
			text = fmt.Sprintf("-%s per run (≈ %s) · %s total", bigquery.FormatBytes(-d), cost.SignedUSD(o.Cost.USD(d)), totalText)
		default:
			text = fmt.Sprintf("+%s per run (≈ %s) · %s total", bigquery.FormatBytes(d), cost.SignedUSD(o.Cost.USD(d)), totalText)
		}
	} else {
		text = fmt.Sprintf("%s per run (≈ %s%s)", totalText, bound, cost.FormatUSD(o.Cost.USD(total)))
	}
	unfiltered := 0
	for _, a := range c.Actions {
		if a.Estimate != nil && len(a.Unfiltered) > 0 {
			unfiltered++
		}
	}
	if unfiltered > 0 {
		text += " · ⚠️ " + count(unfiltered, "query") + " without a partition filter"
	}
	if r.CostTripped(o) {
		text += " · ❌ over the " + bigquery.FormatBytes(o.Cost.Threshold) + " limit"
	}
	return text
}

// count is "1 change" or "2 changes" ("1 query", "2 queries").
func count(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	if strings.HasSuffix(word, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(word, "y"))
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Title is the report's heading: "xf check", with the project's folder
// when it isn't the repository's root.
func Title(project string) string {
	if project == "" || project == "." {
		return "xf check"
	}
	return "xf check · `" + project + "`"
}

// Markdown is the report: its title and verdict, then each check's report
// folded.
func Markdown(r *Report, o Options) string {
	lines := []string{"### " + Title(r.Project), "", "**" + Verdict(r, o) + "**", ""}
	if r.Impact != nil {
		lines = append(lines, "<details><summary>Column impact</summary>", "", demote(impact.Markdown(r.Impact)), "", "</details>", "")
	} else {
		lines = append(lines, "❔ Column impact couldn't be checked: "+oneLine(r.ImpactError), "")
	}
	if r.Cost != nil {
		lines = append(lines, "<details><summary>Query cost</summary>", "", demote(strings.TrimRight(cost.Markdown(r.Cost, o.Cost), "\n")), "", "</details>", "")
	} else {
		lines = append(lines, "❔ Query cost couldn't be checked: "+oneLine(r.CostError), "")
	}
	return strings.Join(lines, "\n")
}

// demote turns a report's ### title into ####, under the check's own.
func demote(md string) string {
	if strings.HasPrefix(md, "### ") {
		return "#" + md
	}
	return md
}

// oneLine is err's first line, for a sentence.
func oneLine(err string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(err), "\n")
	return first
}

// JSON is the report for scripts: impact's and cost's own documents under
// impact and cost, null with an error beside it when one couldn't be made.
func JSON(r *Report, o Options) ([]byte, error) {
	out := struct {
		Version     int             `json:"version"`
		Project     string          `json:"project"`
		Verdict     string          `json:"verdict"`
		Worth       bool            `json:"worthAComment"`
		Impact      json.RawMessage `json:"impact"`
		ImpactError string          `json:"impactError,omitempty"`
		Cost        json.RawMessage `json:"cost"`
		CostError   string          `json:"costError,omitempty"`
	}{Version: JSONVersion, Project: r.Project, Verdict: Verdict(r, o), Worth: r.Worth(o),
		Impact: json.RawMessage("null"), ImpactError: r.ImpactError, Cost: json.RawMessage("null"), CostError: r.CostError}
	if r.Impact != nil {
		data, err := impact.JSON(r.Impact)
		if err != nil {
			return nil, err
		}
		out.Impact = data
	}
	if r.Cost != nil {
		data, err := cost.JSON(r.Cost, o.Cost)
		if err != nil {
			return nil, err
		}
		out.Cost = data
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

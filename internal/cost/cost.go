// Package cost estimates what the branch's Changed Actions would scan in
// BigQuery: every changed action with something to dry run is dry run, as
// it is on the branch and as it was at the merge-base with the default
// branch, and a read of a partitioned table that never names its partition
// column is pointed out. Dry runs cost nothing and run nothing.
//
// An incremental table counts what its incremental run scans; its full
// rebuild is reported beside it. One whose own table isn't built yet counts
// its full build, as its first run is one. An action BigQuery gives no
// estimate for is listed with the reason and counts for nothing.
//
// With defer to prod on (ADR 0031), a table the branch adds that isn't
// built where the dry runs look is read from dev when it is built there.
package cost

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/impact"
)

// Estimate is what BigQuery estimates a query scans.
type Estimate struct {
	Bytes int64 `json:"bytes"`
	// Accuracy is one of the bigquery.Accuracy constants, or "".
	Accuracy string `json:"accuracy,omitempty"`
}

// Bound is "≤ " for an upper bound, "≥ " for a lower bound, else "".
func (e Estimate) Bound() string {
	switch e.Accuracy {
	case bigquery.AccuracyUpperBound:
		return "≤ "
	case bigquery.AccuracyLowerBound:
		return "≥ "
	}
	return ""
}

// Unfiltered is a partitioned table an action reads without naming the
// column it is partitioned by, so no partition can be pruned.
type Unfiltered struct {
	// Table is the project.dataset.table read.
	Table string `json:"table"`
	// Column is its partition column: _PARTITIONTIME for a table
	// partitioned by ingestion time.
	Column string `json:"column"`
}

// Action is the estimate of one Changed Action.
type Action struct {
	// Name is the action's ID: its project.dataset.name.
	Name string `json:"name"`
	// DryRunAs is its project.dataset.name in the compile the dry runs
	// used, when that isn't Name: compiled with prodCompilerOptions, a
	// branch's tables are named in prod's project there.
	DryRunAs string `json:"dryRunAs,omitempty"`
	FileName string `json:"fileName,omitempty"`
	// Type is as changes.Changed's.
	Type string `json:"type"`
	// Estimate is what a run of the action scans: an incremental table's
	// incremental run. It is nil when Reason says why there is none.
	Estimate *Estimate `json:"estimate,omitempty"`
	// Full is what an incremental table's full rebuild scans.
	Full *Estimate `json:"full,omitempty"`
	// New is set on an action the merge-base doesn't have.
	New bool `json:"new,omitempty"`
	// Base is what the action scanned at the merge-base; nil for a new
	// action, and when it could not be estimated.
	Base *Estimate `json:"base,omitempty"`
	// Unfiltered are the partitioned tables Estimate's query reads without
	// naming their partition column.
	Unfiltered []Unfiltered `json:"unfiltered,omitempty"`
	// Reason is why the action has no Estimate.
	Reason string `json:"reason,omitempty"`
	// FirstBuild says Estimate is of an incremental table's full build:
	// its own table isn't in BigQuery yet, so its first run builds it
	// whole, as Dataform does.
	FirstBuild bool `json:"firstBuild,omitempty"`
	// Incremental is, with FirstBuild, what each run after the first
	// scans: the incremental run, reading dev's copy of the table. It is
	// nil when dev has none, or the run couldn't be estimated.
	Incremental *Estimate `json:"incremental,omitempty"`
	// FromDev are the tables the branch adds that the dry runs read from
	// dev, as they aren't where the dry runs look (Deps.InDev): the
	// estimate is of dev's data.
	FromDev []DevRead `json:"fromDev,omitempty"`
}

// DevRead is a table the branch adds that a dry run read from dev.
type DevRead struct {
	// Table is its project.dataset.name where the dry runs look; Dev is
	// its table in dev.
	Table string `json:"table"`
	Dev   string `json:"dev"`
	// BuiltAt is when it was built in dev; zero when not known.
	BuiltAt time.Time `json:"builtAt,omitzero"`
}

// RunKind names which of an action's runs an estimate is.
type RunKind string

const (
	RunFull        RunKind = "full"
	RunIncremental RunKind = "incremental"
)

// Runs are what the action's full run and incremental run scan, nil
// where not known, and which of them the total counts. A table or view
// only has a full run; an incremental table counts its incremental run,
// or its full build while its table isn't built yet.
func (a Action) Runs() (full, incremental *Estimate, counted RunKind) {
	switch {
	case a.Estimate == nil:
		return nil, nil, ""
	case a.FirstBuild:
		return a.Estimate, a.Incremental, RunFull
	case a.Type == "incremental":
		return a.Full, a.Estimate, RunIncremental
	}
	return a.Estimate, nil, RunFull
}

// Delta is how many more bytes the action scans than at the merge-base; ok
// is false when either side has no estimate.
func (a Action) Delta() (bytes int64, ok bool) {
	if a.Estimate == nil || a.Base == nil {
		return 0, false
	}
	return a.Estimate.Bytes - a.Base.Bytes, true
}

// Report is the query cost of a branch's Changed Actions.
type Report struct {
	Comparison *impact.Comparison `json:"comparison,omitempty"`
	// Compiled says what the dry runs were compiled with when it isn't the
	// project's own settings: "prodCompilerOptions".
	Compiled string `json:"compiled,omitempty"`
	// DryRunIn is the project the dry runs ran in, with their location:
	// "acme-prod (EU)". It is "" when they ran in more than one.
	DryRunIn string `json:"dryRunIn,omitempty"`
	// Unset is the setting that picks the tables the dry runs read,
	// "prodCompilerOptions" or "dbtProdTarget", when it isn't set anywhere, not
	// even to "": they read what the defaults resolve to, which may be dev
	// tables.
	Unset string `json:"unset,omitempty"`
	// Actions are the changed actions with something to dry run: the
	// estimated ones, most bytes first, then the others by name.
	Actions []Action `json:"actions"`
	// BasePending is set on a report sent ahead of its merge-base
	// estimates (see Progress.Partial).
	BasePending bool `json:"basePending,omitempty"`
	// BaseError is why no action has a Base, when the merge-base could not
	// be compiled.
	BaseError string    `json:"baseError,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	// Signature is what was checked (see impact.Signature).
	Signature string `json:"signature"`
	// Stale is set when the branch has changed since.
	Stale bool `json:"stale,omitempty"`
}

// Progress is how far a check got.
type Progress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
	// Partial is the report so far, once the branch has been estimated and
	// only the merge-base is left.
	Partial *Report `json:"partial,omitempty"`
}

// Total is the bytes the estimated actions scan between them. bound is
// "≥ " when a lower bound is among them, "≤ " when the only inexact ones are
// upper bounds, else "".
func (r *Report) Total() (bytes int64, bound string) {
	var es []Estimate
	for _, a := range r.Actions {
		if a.Estimate != nil {
			es = append(es, *a.Estimate)
		}
	}
	return sum(es)
}

// Later is the total of a run after the first, once the first has built
// the incremental tables that are first builds: Total with their
// incremental runs for their full builds. ok is false when no action is a
// first build, or one's incremental run isn't known.
func (r *Report) Later() (bytes int64, bound string, ok bool) {
	var es []Estimate
	for _, a := range r.Actions {
		switch {
		case a.Estimate == nil:
		case !a.FirstBuild:
			es = append(es, *a.Estimate)
		case a.Incremental == nil:
			return 0, "", false
		default:
			es = append(es, *a.Incremental)
			ok = true
		}
	}
	if !ok {
		return 0, "", false
	}
	bytes, bound = sum(es)
	return bytes, bound, true
}

// BaseTotal is what the actions Total counts scanned at the merge-base, a
// new action nothing; ok is false when one of the others has no Base, or
// the merge-base wasn't estimated.
func (r *Report) BaseTotal() (bytes int64, ok bool) {
	if r.BaseError != "" || r.BasePending {
		return 0, false
	}
	for _, a := range r.Actions {
		switch {
		case a.Estimate == nil || a.New:
		case a.Base == nil:
			return 0, false
		default:
			bytes += a.Base.Bytes
		}
	}
	return bytes, true
}

// sum adds es up, with the bound Total says.
func sum(es []Estimate) (bytes int64, bound string) {
	for _, e := range es {
		bytes += e.Bytes
		switch e.Accuracy {
		case bigquery.AccuracyLowerBound:
			bound = "≥ "
		case bigquery.AccuracyUpperBound:
			if bound == "" {
				bound = "≤ "
			}
		}
	}
	return bytes, bound
}

// ReadFromDev counts the tables the branch adds that estimated actions
// read from dev.
func (r *Report) ReadFromDev() int {
	tables := map[string]bool{}
	for _, a := range r.Actions {
		if a.Estimate == nil {
			continue
		}
		for _, d := range a.FromDev {
			tables[d.Table] = true
		}
	}
	return len(tables)
}

// FromDevLine says how many tables the branch adds the estimates read
// from dev, "" when none.
func FromDevLine(r *Report) string {
	n := r.ReadFromDev()
	if n == 0 {
		return ""
	}
	return "defer to prod: " + plural(n, "new table", "new tables") + " read from dev, sized as dev has " + map[bool]string{true: "it", false: "them"}[n == 1]
}

// Unestimated counts the actions without an estimate.
func (r *Report) Unestimated() int {
	n := 0
	for _, a := range r.Actions {
		if a.Estimate == nil {
			n++
		}
	}
	return n
}

// Exceeds reports whether the report's total is over threshold bytes; a
// threshold of 0 is no limit.
func (r *Report) Exceeds(threshold int64) bool {
	total, _ := r.Total()
	return threshold > 0 && total > threshold
}

// sortActions puts the estimated actions first, most bytes first, then the
// others; ties go by name.
func sortActions(actions []Action) {
	slices.SortStableFunc(actions, func(a, b Action) int {
		switch {
		case a.Estimate != nil && b.Estimate != nil:
			return cmp.Or(cmp.Compare(b.Estimate.Bytes, a.Estimate.Bytes), cmp.Compare(a.Name, b.Name))
		case a.Estimate != nil:
			return -1
		case b.Estimate != nil:
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
}

var sizeRE = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)\s*(b|[kmgtp]i?b?)?$`)

// ParseSize reads a size such as 100GB, 1.5 TiB or 2048: a number and a
// unit from B to PB, bytes without one. Units are binary whether or not
// they are written with an i, as BigQuery bills: 1 GB is 2³⁰ bytes.
func ParseSize(s string) (int64, error) {
	m := sizeRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("%q is not a size: write a number and a unit, such as 100GB or 1.5TiB", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size: %w", s, err)
	}
	if unit := strings.ToLower(m[2]); unit != "" && unit != "b" {
		n *= float64(int64(1) << (10 * (strings.IndexByte("kmgtp", unit[0]) + 1)))
	}
	if n >= 1<<63 {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return int64(n), nil
}

package frontend

import (
	"fmt"
	"strings"

	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/impact"
)

// CostSkipped is a cost report's actions without an estimate, as xf web's
// cost tab lays them out by root cause:
//  1. Missing: actions that read a table not in BigQuery (and not added on
//     this branch), which need checking.
//  2. Waiting: new tables added on this branch that haven't been built yet,
//     with the downstream tables and assertions waiting on each one.
//  3. Other: actions that couldn't be priced for another reason (such as
//     queries over external tables or scripts), grouped by reason.
type CostSkipped struct {
	Count int        `json:"count"`
	Chips []CostChip `json:"chips"`
	// Where is the project and location the missing tables were looked
	// for in, "acme-prod (EU)"; "" when there is more than one, and the
	// Reads of Missing and Waiting then keep their project.
	Where   string             `json:"where,omitempty"`
	Missing []CostMissingRow   `json:"missing,omitempty"`
	Waiting []CostWaitingGroup `json:"waiting,omitempty"`
	Other   []CostOtherGroup   `json:"other,omitempty"`
}

// CostChip counts the actions of one kind: "new", "missing" or "other".
type CostChip struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// CostMissingRow is an action whose dry run read a table BigQuery doesn't
// have (excluding tables added on this branch, which go in Waiting), or the
// assertions that read the same one, folded into one row.
type CostMissingRow struct {
	Action     string   `json:"action,omitempty"`
	Assertions []string `json:"assertions,omitempty"`
	Type       string   `json:"type"`
	Reads      string   `json:"reads"`
	Status     string   `json:"status"`
	Note       string   `json:"note"`
	Kind       string   `json:"kind"`
}

// CostWaitingGroup is a new table added on this branch and the changed
// actions whose dry runs wait on a run to build it first.
type CostWaitingGroup struct {
	Reads      string              `json:"reads"`
	Count      int                 `json:"count"`
	Actions    []CostWaitingAction `json:"actions,omitempty"`
	Assertions []string            `json:"assertions,omitempty"`
}

// CostWaitingAction is a non-assertion action waiting on a new branch table.
type CostWaitingAction struct {
	Action string `json:"action"`
	Type   string `json:"type"`
}

// CostOtherGroup is the actions not estimated for one non-missing reason
// (such as external tables or scripts), grouped by Why.
type CostOtherGroup struct {
	Why     string            `json:"why"`
	Actions []CostOtherAction `json:"actions"`
}

// CostOtherAction is one action inside a CostOtherGroup.
type CostOtherAction struct {
	Action string `json:"action"`
	Type   string `json:"type"`
	New    bool   `json:"new,omitempty"`
}

const noBytes = "dry run worked, but BigQuery gave no byte count: it can't size some queries ahead, such as ones over external tables"

// costSkipped lays out r's actions without an estimate; nil when it has none.
func costSkipped(r *cost.Report) *CostSkipped {
	skipped := cost.SkippedActions(r)
	if len(skipped) == 0 {
		return nil
	}
	v := &CostSkipped{Count: len(skipped)}
	places := map[string]bool{}
	for _, s := range skipped {
		if s.Missing != "" {
			project, _, _ := strings.Cut(s.Missing, ".")
			v.Where = project + " (" + s.Location + ")"
			places[v.Where] = true
		}
	}
	if len(places) > 1 {
		v.Where = ""
	}
	counts := map[string]int{}
	foldedMissing := map[string]int{} // a missing table's row of assertions
	waitingByTable := map[string]int{}
	otherByWhy := map[string]int{}

	for _, s := range skipped {
		name := impact.ShortTable(s.Action.Name)
		if s.Missing == "" {
			why, kind := s.Why, "failed"
			if strings.HasPrefix(why, "BigQuery gave no estimate") || strings.HasPrefix(why, "BigQuery gives no estimate") {
				kind = "noBytes"
				if why == "BigQuery gave no estimate" {
					why = noBytes
				}
			}
			counts[kind]++
			idx, ok := otherByWhy[why]
			if !ok {
				idx = len(v.Other)
				otherByWhy[why] = idx
				v.Other = append(v.Other, CostOtherGroup{Why: why})
			}
			v.Other[idx].Actions = append(v.Other[idx].Actions, CostOtherAction{
				Action: name,
				Type:   s.Action.Type,
				New:    s.Action.New,
			})
			continue
		}

		reads := s.Missing
		if v.Where != "" {
			reads = impact.ShortTable(s.Missing)
		}

		if s.New {
			counts["new"]++
			idx, ok := waitingByTable[reads]
			if !ok {
				idx = len(v.Waiting)
				waitingByTable[reads] = idx
				v.Waiting = append(v.Waiting, CostWaitingGroup{Reads: reads})
			}
			g := &v.Waiting[idx]
			g.Count++
			if s.Action.Type == "assertion" {
				g.Assertions = append(g.Assertions, name)
			} else {
				g.Actions = append(g.Actions, CostWaitingAction{Action: name, Type: s.Action.Type})
			}
			continue
		}

		row := CostMissingRow{
			Action: name,
			Type:   s.Action.Type,
			Reads:  reads,
			Status: "missing",
			Kind:   "missing",
			Note:   "not on this branch: check it",
		}
		if s.Changed {
			row.Note = "changed on this branch"
		}
		counts["missing"]++
		if s.Action.Type == "assertion" {
			if i, ok := foldedMissing[s.Missing]; ok {
				r := &v.Missing[i]
				if r.Action != "" {
					r.Assertions, r.Action = []string{r.Action}, ""
				}
				r.Assertions = append(r.Assertions, name)
				continue
			}
			foldedMissing[s.Missing] = len(v.Missing)
		}
		v.Missing = append(v.Missing, row)
	}

	for _, c := range []struct{ kind, one, many, chip string }{
		{"missing", "reads a missing table", "read a missing table", "missing"},
		{"new", "waits on a new table", "wait on a new table", "new"},
		{"noBytes", "gave no byte count", "gave no byte count", "other"},
		{"failed", "couldn't be dry run", "couldn't be dry run", "other"},
	} {
		if n := counts[c.kind]; n > 0 {
			text := c.many
			if n == 1 {
				text = c.one
			}
			v.Chips = append(v.Chips, CostChip{Text: fmt.Sprintf("%d %s", n, text), Kind: c.chip})
		}
	}
	return v
}

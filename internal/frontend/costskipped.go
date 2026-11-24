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
//     with the downstream tables and assertions waiting on each one; one
//     that waits on two is under both.
//  3. Columns: columns this branch adds to a changed table that dry runs
//     didn't find, with the actions waiting on each.
//  4. Other: actions that couldn't be priced for another reason (such as
//     queries over external tables or scripts), grouped by reason.
type CostSkipped struct {
	Count int        `json:"count"`
	Chips []CostChip `json:"chips"`
	// Where is the project and location the missing tables were looked
	// for in, "acme-prod (EU)"; "" when there is more than one, and the
	// Reads of Missing and Waiting then keep their project.
	Where   string             `json:"where,omitempty"`
	Roots   []CostRootGroup    `json:"roots,omitempty"`
	Missing []CostMissingRow   `json:"missing,omitempty"`
	Waiting []CostWaitingGroup `json:"waiting,omitempty"`
	// Waits is the actions in Waiting: one waiting on two new tables is
	// under each.
	Waits int `json:"waits,omitempty"`
	// Columns are the columns this branch adds to a changed table that
	// dry runs didn't find, with the actions waiting on each.
	Columns []CostColumnGroup `json:"columns,omitempty"`
	Other   []CostOtherGroup  `json:"other,omitempty"`
}

// CostRootGroup is a deepest missing table in the dependency graph blocking
// one or more unestimated actions, with direct and transitive cascades.
type CostRootGroup struct {
	Table  string            `json:"table"`
	Kind   string            `json:"kind"` // "missing", "prereq", "neverBuilt", "new"
	Status string            `json:"status"`
	Note   string            `json:"note,omitempty"`
	Count  int               `json:"count"`
	Direct []string          `json:"direct,omitempty"`
	Via    []CostBlockingVia `json:"via,omitempty"`
}

// CostBlockingVia lists actions blocked through an intermediate missing table.
type CostBlockingVia struct {
	Table   string   `json:"table"`
	Actions []string `json:"actions"`
}

// CostColumnGroup is a column a changed action makes on this branch, which
// its table in BigQuery hasn't got until a run builds it, and the actions
// whose dry runs wait on it.
type CostColumnGroup struct {
	Column  string              `json:"column"`
	Table   string              `json:"table"`
	Actions []CostWaitingAction `json:"actions"`
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
	for _, br := range r.BlockingRoots {
		if br.Table != "" && br.Location != "" {
			project, _, _ := strings.Cut(br.Table, ".")
			v.Where = project + " (" + br.Location + ")"
			places[v.Where] = true
		}
	}
	if len(places) > 1 {
		v.Where = ""
	}
	short := func(table string) string {
		if v.Where != "" {
			return impact.ShortTable(table)
		}
		return table
	}
	counts := map[string]int{}
	foldedMissing := map[string]int{} // a missing table's row of assertions
	waitingByTable := map[string]int{}
	otherByWhy := map[string]int{}
	columnAt := map[cost.AddedColumn]int{}
	useRoots := len(r.BlockingRoots) > 0

	if useRoots {
		for _, br := range r.BlockingRoots {
			rg := CostRootGroup{
				Table: short(br.Table),
				Count: br.BlockedCount(),
			}
			switch br.Kind {
			case cost.MissingPrereq:
				rg.Kind, rg.Status = "prereq", "prerequisite"
				var detail []string
				if br.From != "" {
					detail = append(detail, "retargeted from "+short(br.From))
				}
				if br.FileName != "" {
					detail = append(detail, "declared in "+br.FileName)
				}
				if len(detail) > 0 {
					rg.Note = strings.Join(detail, " · ") + " · built outside Dataform, load before running"
				} else {
					rg.Note = "built outside Dataform, load before running"
				}
			case cost.MissingNeverBuilt:
				rg.Kind, rg.Status, rg.Note = "neverBuilt", "never built", "never built in prod"
			case cost.MissingNew:
				rg.Kind, rg.Status, rg.Note = "new", "not built yet", "added on this branch"
			default:
				rg.Kind, rg.Status, rg.Note = "missing", "missing", "not touched by this branch: check it"
			}
			for _, a := range br.Direct {
				rg.Direct = append(rg.Direct, impact.ShortTable(a))
			}
			for _, via := range br.Via {
				var acts []string
				for _, a := range via.Actions {
					acts = append(acts, impact.ShortTable(a))
				}
				rg.Via = append(rg.Via, CostBlockingVia{Table: short(via.Table), Actions: acts})
			}
			v.Roots = append(v.Roots, rg)
		}
	}

	for _, s := range skipped {
		name := impact.ShortTable(s.Action.Name)
		if c := s.Column; c != nil {
			counts["column"]++
			idx, ok := columnAt[*c]
			if !ok {
				idx = len(v.Columns)
				columnAt[*c] = idx
				v.Columns = append(v.Columns, CostColumnGroup{Column: c.Column, Table: impact.ShortTable(c.Table)})
			}
			v.Columns[idx].Actions = append(v.Columns[idx].Actions, CostWaitingAction{Action: name, Type: s.Action.Type})
			continue
		}
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
		if useRoots {
			continue
		}

		reads := short(s.Missing)

		if s.New {
			counts["new"]++
			// Under each table it waits on.
			for _, t := range s.Waits {
				t = short(t)
				idx, ok := waitingByTable[t]
				if !ok {
					idx = len(v.Waiting)
					waitingByTable[t] = idx
					v.Waiting = append(v.Waiting, CostWaitingGroup{Reads: t})
				}
				g := &v.Waiting[idx]
				g.Count++
				if s.Action.Type == "assertion" {
					g.Assertions = append(g.Assertions, name)
				} else {
					g.Actions = append(g.Actions, CostWaitingAction{Action: name, Type: s.Action.Type})
				}
			}
			continue
		}

		row := CostMissingRow{
			Action: name,
			Type:   s.Action.Type,
			Reads:  reads,
			Status: "missing",
			Kind:   "missing",
			Note:   "not touched by this branch: check it",
		}
		switch s.Kind {
		case cost.MissingPrereq:
			row.Status, row.Kind = "prerequisite", "prereq"
			if s.FileName != "" {
				row.Note = "declared in " + s.FileName + ": load before running"
			} else {
				row.Note = "declared on this branch: load before running"
			}
		case cost.MissingNeverBuilt:
			row.Status, row.Kind = "never built", "neverBuilt"
			row.Note = "never built in prod"
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

	v.Waits = counts["new"]
	if useRoots {
		blocked := map[string]bool{}
		for _, br := range r.BlockingRoots {
			for _, a := range br.Direct {
				blocked[a] = true
			}
			for _, via := range br.Via {
				for _, a := range via.Actions {
					blocked[a] = true
				}
			}
		}
		chipKind := "missing"
		allPrereq, allNew := true, true
		for _, rg := range v.Roots {
			if rg.Kind != "prereq" {
				allPrereq = false
			}
			if rg.Kind != "new" && rg.Kind != "neverBuilt" {
				allNew = false
			}
		}
		nounOne, nounMany := "root cause", "root causes"
		if allPrereq {
			chipKind, nounOne, nounMany = "prereq", "missing prerequisite", "missing prerequisites"
		} else if allNew {
			chipKind, nounOne, nounMany = "new", "unbuilt branch table", "unbuilt branch tables"
		}
		nRoots, nBlocked := len(v.Roots), len(blocked)
		noun, verb := nounMany, "block"
		if nRoots == 1 {
			noun, verb = nounOne, "blocks"
		}
		actNoun := "actions"
		if nBlocked == 1 {
			actNoun = "action"
		}
		v.Chips = append(v.Chips, CostChip{Text: fmt.Sprintf("%d %s %s %d %s", nRoots, noun, verb, nBlocked, actNoun), Kind: chipKind})
	}
	for _, c := range []struct{ kind, one, many, chip string }{
		{"missing", "reads a missing table", "read a missing table", "missing"},
		{"new", "waits on a new table", "wait on a new table", "new"},
		{"column", "waits on a new column", "wait on a new column", "new"},
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

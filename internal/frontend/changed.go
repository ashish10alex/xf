package frontend

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// ListItem is a Changed or Deleted action as the overlay lists it.
type ListItem struct {
	ID       project.ActionID
	Parent   project.ActionID // the table a generated assertion comes from
	FileName string
	Type     string
	Reasons  []changes.Reason
	// Renamed: the action is new under the name of its Renamed file.
	Renamed bool
}

// ListGroup is a row of the overlay: an action with the assertions Dataform
// generated from its config (uniqueKey, nonNull, rowConditions) folded
// under it, since their names are long and say little on their own. When
// only the assertions changed, the action is there for them (listed is
// false).
type ListGroup struct {
	ListItem
	Listed  bool
	Asserts []ListItem
}

// GroupItems folds generated assertions under their tables, keeping the
// order of items (by file, then name).
func GroupItems(items []ListItem) []*ListGroup {
	var groups []*ListGroup
	byID := map[project.ActionID]*ListGroup{}
	get := func(id project.ActionID, file string) *ListGroup {
		g := byID[id]
		if g == nil {
			g = &ListGroup{ListItem: ListItem{ID: id, FileName: file}}
			byID[id] = g
			groups = append(groups, g)
		}
		return g
	}
	for _, it := range items {
		if it.Parent != "" {
			g := get(it.Parent, it.FileName)
			g.Asserts = append(g.Asserts, it)
			continue
		}
		g := get(it.ID, it.FileName)
		asserts := g.Asserts
		g.ListItem, g.Listed, g.Asserts = it, true, asserts
	}
	return groups
}

// AssertionsSummary is "2 assertions: rowConditions, uniqueKey ×2".
func AssertionsSummary(asserts []ListItem) string {
	var kinds []string
	count := map[string]int{}
	for _, a := range asserts {
		k := AssertionKind(a.ID)
		if count[k] == 0 {
			kinds = append(kinds, k)
		}
		count[k]++
	}
	for i, k := range kinds {
		if count[k] > 1 {
			kinds[i] = fmt.Sprintf("%s ×%d", k, count[k])
		}
	}
	n := len(asserts)
	return fmt.Sprintf("%d assertion%s: %s", n, Plural(n), strings.Join(kinds, ", "))
}

// ReasonsOf is the reasons of items, each once, in the order of
// changes.Reasons.
func ReasonsOf(items []ListItem) string {
	seen := map[changes.Reason]bool{}
	renamed := false
	for _, it := range items {
		for _, r := range it.Reasons {
			if r == changes.ReasonNew && it.Renamed {
				renamed = true
				continue
			}
			seen[r] = true
		}
	}
	var out []string
	for _, r := range changes.Reasons {
		if seen[r] {
			out = append(out, string(r))
		}
		if r == changes.ReasonNew && renamed {
			out = append(out, RenamedReason)
		}
	}
	return strings.Join(out, ", ")
}

// RowKind is what a row of the overlay's list shows.
type RowKind int

// ListRow is a row of the overlay's list.
type ListRow struct {
	Kind  RowKind
	File  string
	Group *ListGroup // nil for a fileRow
}

// Key tells the row from the others, the same in a list asked for again.
func (r ListRow) Key() string {
	switch r.Kind {
	case FileRow:
		return "file " + r.File
	case AssertsRow:
		return "assertions " + string(r.Group.ID)
	}
	return "action " + string(r.Group.ID)
}

// GroupRows lays groups out under their files: a row per file, then a row
// per action and one for the assertions generated from it.
func GroupRows(groups []*ListGroup) []ListRow {
	var rows []ListRow
	file := "\x00"
	for _, g := range groups {
		if g.FileName != file {
			file = g.FileName
			rows = append(rows, ListRow{Kind: FileRow, File: file})
		}
		rows = append(rows, ListRow{Kind: ActionRow, File: file, Group: g})
		if len(g.Asserts) > 0 {
			rows = append(rows, ListRow{Kind: AssertsRow, File: file, Group: g})
		}
	}
	return rows
}

// ChangedListRows is the rows of the Changed Actions.
func ChangedListRows(res *changes.Result) []ListRow {
	items := make([]ListItem, len(res.Changed))
	for i, c := range res.Changed {
		items[i] = ListItem{ID: c.ID, Parent: c.Parent, FileName: c.FileName, Type: c.Type, Reasons: c.Reasons, Renamed: c.RenamedFrom != ""}
	}
	return GroupRows(GroupItems(items))
}

const (
	FileRow    RowKind = iota // a file, over its actions
	ActionRow                 // an action
	AssertsRow                // the assertions generated from the action above
)

// ChangedRow is a row of the R overlay's list: a file over its actions, an
// action, the assertions generated from the action above, or the heading
// of the actions removed on the branch.
type ChangedRow struct {
	// Kind is "file", "action", "asserts" or "removed" (the heading).
	Kind string `json:"kind"`
	// File is the row's file, and From what git says it was renamed from:
	// "← old.sqlx (95%)".
	File string `json:"file,omitempty"`
	From string `json:"from,omitempty"`
	// ID is an action row's action, Label its "dataset.name", or an
	// asserts row's summary, and Tail what is right-aligned after it.
	ID    project.ActionID `json:"id,omitempty"`
	Label string           `json:"label,omitempty"`
	Tail  string           `json:"tail,omitempty"`
	// Mark is the action's marker: "●" changed, "○" there for its
	// assertions, "✗" removed.
	Mark string `json:"mark,omitempty"`
	// Removed says the row is of the removed actions, which don't run.
	Removed bool `json:"removed,omitempty"`
}

// ChangedRows lists res as the R overlay does: the Changed Actions under
// their files, then the actions removed on the branch.
func ChangedRows(res *changes.Result) []ChangedRow {
	var out []ChangedRow
	renames := RenamedFrom(res)
	for _, r := range ChangedListRows(res) {
		out = append(out, changedRow(r, renames, false, nil))
	}
	deleted := Removed(res)
	if len(deleted) == 0 {
		return out
	}
	items := make([]ListItem, len(deleted))
	for i, d := range deleted {
		items[i] = ListItem{ID: d.ID, Parent: d.Parent, FileName: d.FileName, Type: d.Type}
	}
	renamedTo := map[project.ActionID]bool{}
	for _, d := range res.Deleted {
		if d.RenamedTo != "" {
			renamedTo[d.ID] = true
		}
	}
	out = append(out, ChangedRow{Kind: "removed", Label: "removed on this branch, not run", Removed: true})
	for _, r := range GroupRows(GroupItems(items)) {
		out = append(out, changedRow(r, nil, true, renamedTo))
	}
	return out
}

func changedRow(r ListRow, renames map[string]string, removed bool, renamedTo map[project.ActionID]bool) ChangedRow {
	row := ChangedRow{File: r.File, Removed: removed}
	switch r.Kind {
	case FileRow:
		row.Kind, row.File, row.From = "file", cmp.Or(r.File, "(no file)"), renames[r.File]
	case AssertsRow:
		row.Kind, row.ID = "asserts", r.Group.ID
		row.Label, row.Tail = "↳ "+AssertionsSummary(r.Group.Asserts), ReasonsOf(r.Group.Asserts)
	default:
		schema, name := SplitID(r.Group.ID)
		row.Kind, row.ID, row.Label = "action", r.Group.ID, strings.TrimPrefix(schema+"."+name, ".")
		switch {
		case removed && !r.Group.Listed && renamedTo[r.Group.ID]:
			row.Mark, row.Tail = "○", RenamedReason
		case removed && !r.Group.Listed:
			row.Mark, row.Tail = "○", "kept"
		case removed:
			row.Mark, row.Tail = "✗", r.Group.Type
		case !r.Group.Listed:
			row.Mark, row.Tail = "○", "unchanged"
		default:
			row.Mark, row.Tail = "●", r.Group.Type+" · "+ReasonsOf([]ListItem{r.Group.ListItem})
		}
	}
	return row
}

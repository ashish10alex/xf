package frontend

import (
	"cmp"
	"slices"
	"strings"

	"github.com/sahilm/fuzzy"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

// ActionItem is an action as the palette lists it.
type ActionItem struct {
	Action *project.Action
	Label  string // "schema.name"
}

// ActionItems labels every action by its target, sorted by label.
func ActionItems(g *project.Graph) []ActionItem {
	var items []ActionItem
	for _, a := range g.Actions {
		if a.Kind != project.KindUnknown {
			items = append(items, ActionItem{Action: a, Label: a.Target.Schema + "." + a.Target.Name})
		}
	}
	slices.SortFunc(items, func(x, y ActionItem) int {
		return cmp.Or(cmp.Compare(strings.ToLower(x.Label), strings.ToLower(y.Label)), cmp.Compare(x.Action.ID, y.Action.ID))
	})
	return items
}

// FirstItems is items with the actions of first ahead of the rest, in
// first's order: the palette's order for an empty query.
func FirstItems(items []ActionItem, first []*project.Action) []ActionItem {
	rank := map[project.ActionID]int{}
	for _, a := range first {
		if _, ok := rank[a.ID]; !ok {
			rank[a.ID] = len(rank)
		}
	}
	out := slices.Clone(items)
	slices.SortStableFunc(out, func(x, y ActionItem) int {
		rx, okx := rank[x.Action.ID]
		ry, oky := rank[y.Action.ID]
		switch {
		case okx && oky:
			return cmp.Compare(rx, ry)
		case okx:
			return -1
		case oky:
			return 1
		}
		return 0
	})
	return out
}

// KindFilter is a "$name" word of the palette's query: it keeps the actions
// of its kinds.
type KindFilter struct {
	Name, Short string
	Kinds       []project.ActionKind
}

// KindFilters lists the kind filters of a tool ("dataform" or "dbt"), in
// display order: one for each kind the tool has, named as the kind is with
// any space dropped ("$materializedview") and abbreviated as in the lists
// ("$mv"). dbt adds "$model" for every materialization of a model.
func KindFilters(tool string) []KindFilter {
	kinds := []project.ActionKind{
		project.KindTable, project.KindView, project.KindIncremental, project.KindMaterializedView,
		project.KindOperation, project.KindAssertion, project.KindNotebook, project.KindPropertyGraph,
		project.KindDeclaration,
	}
	var filters []KindFilter
	if tool == "dbt" {
		models := []project.ActionKind{
			project.KindTable, project.KindView, project.KindIncremental, project.KindMaterializedView,
			project.KindEphemeral,
		}
		filters = append(filters, KindFilter{Name: "model", Kinds: models})
		kinds = append(models, project.KindSeed, project.KindSnapshot, project.KindTest, project.KindAnalysis,
			project.KindOperation, project.KindSource, project.KindExposure)
	}
	for _, k := range kinds {
		filters = append(filters, KindFilter{
			Name:  strings.ReplaceAll(k.String(), " ", ""),
			Short: k.Short(),
			Kinds: []project.ActionKind{k},
		})
	}
	return filters
}

// KindWords are the words "$" can name in the palette's query, in order.
func KindWords(tool string) []string {
	var out []string
	for _, f := range KindFilters(cmp.Or(tool, "dataform")) {
		out = append(out, f.Name)
	}
	return out
}

// FindActions is the palette's search: the actions query finds in g, best
// first, with first ahead of the rest for an empty query (see
// FilterActions). badKind is a "$" word of the query that names no kind.
func FindActions(g *project.Graph, tool, query string, first []*project.Action) (found []*project.Action, badKind string) {
	items, badKind := FilterActions(FirstItems(ActionItems(g), first), KindFilters(cmp.Or(tool, "dataform")), query)
	for _, it := range items {
		found = append(found, it.Action)
	}
	return found, badKind
}

// FilterActions lists the items of base matching query, base being in the
// empty query's order. Like VS Code's quick open, the query is split on
// spaces and every word must fuzzy-match the label or the file path, in any
// order, so "0500 pos holds" finds 0500_POS_ORDERS_IN_HOLD in
// t2-pos-holds/. A word matches one of the two on its own, never
// letters spread across both.
//
// Each word weighs, best first: as written in the label, as written in the
// path, scattered through the label, scattered through the path. Items rank
// by total weight, then by fuzzy score for the scattered words, then shorter
// labels (a table before the assertions generated from its file), ties
// keeping the empty-query order.
//
// A word that starts with "$" narrows the list to a kind instead:
// "$view" lists the views and "$view orders" the views matching "orders". It
// names every kind it is the start of, or the one it abbreviates, so "$t" is
// tables (and, in dbt, tests) and "$mv" materialized views. Several add up:
// "$table $view" lists both. One that names no kind, as "$" alone does,
// lists nothing, and is badKind.
func FilterActions(base []ActionItem, kinds []KindFilter, query string) (found []ActionItem, badKind string) {
	var terms []string
	var only map[project.ActionKind]bool
	for _, t := range strings.Fields(strings.ToLower(query)) {
		name, ok := strings.CutPrefix(t, "$")
		if !ok {
			terms = append(terms, t)
			continue
		}
		if only == nil {
			only = map[project.ActionKind]bool{}
		}
		hit := false
		for _, f := range kinds {
			if name == "" { // a bare "$" asks which kinds there are
				break
			}
			if strings.HasPrefix(f.Name, name) || f.Short == name {
				hit = true
				for _, k := range f.Kinds {
					only[k] = true
				}
			}
		}
		if !hit {
			badKind = t
		}
	}
	if only != nil {
		var kept []ActionItem
		for _, it := range base {
			if only[it.Action.Kind] {
				kept = append(kept, it)
			}
		}
		base = kept
	}
	if len(terms) == 0 {
		return slices.Clone(base), badKind
	}
	type ranked struct{ i, words, weight, score int }
	rs := make([]ranked, len(base))
	for i := range rs {
		rs[i].i = i
	}
	for _, t := range terms {
		inLabel := map[int]int{}
		for _, m := range fuzzy.FindFrom(t, itemLabels(base)) {
			inLabel[m.Index] = m.Score
		}
		inPath := map[int]int{}
		for _, m := range fuzzy.FindFrom(t, itemPaths(base)) {
			inPath[m.Index] = m.Score
		}
		for i, it := range base {
			r := &rs[i]
			labelScore, inL := inLabel[i]
			pathScore, inP := inPath[i]
			switch {
			case strings.Contains(strings.ToLower(it.Label), t):
				r.weight += 3
			case strings.Contains(strings.ToLower(it.Action.FileName), t):
				r.weight += 2
			case inL:
				r.weight, r.score = r.weight+1, r.score+labelScore
			case inP:
				r.score += pathScore
			default:
				continue
			}
			r.words++
		}
	}
	rs = slices.DeleteFunc(rs, func(r ranked) bool { return r.words < len(terms) })
	slices.SortStableFunc(rs, func(x, y ranked) int {
		return cmp.Or(cmp.Compare(y.weight, x.weight), cmp.Compare(y.score, x.score),
			cmp.Compare(len(base[x.i].Label), len(base[y.i].Label)))
	})
	found = make([]ActionItem, len(rs))
	for j, r := range rs {
		found[j] = base[r.i]
	}
	return found, badKind
}

type itemLabels []ActionItem

func (l itemLabels) String(i int) string { return l[i].Label }
func (l itemLabels) Len() int            { return len(l) }

type itemPaths []ActionItem

func (l itemPaths) String(i int) string { return l[i].Action.FileName }
func (l itemPaths) Len() int            { return len(l) }

// Tag is a tag of the project and the actions a run of it starts with: the
// runnable, enabled ones that have it.
type Tag struct {
	Name    string             `json:"name"`
	Actions []project.ActionID `json:"actions"`
}

// Tags lists the tags of g's actions by name.
func Tags(g *project.Graph) []Tag {
	byName := map[string][]project.ActionID{}
	for _, a := range g.Actions {
		for _, t := range a.Tags {
			ids := byName[t] // a tag of declarations only is listed, with none
			if a.Runnable() && !a.Disabled && !slices.Contains(ids, a.ID) {
				ids = append(ids, a.ID)
			}
			byName[t] = ids
		}
	}
	items := make([]Tag, 0, len(byName))
	for name, ids := range byName {
		items = append(items, Tag{Name: name, Actions: ids})
	}
	slices.SortFunc(items, func(x, y Tag) int { return strings.Compare(x.Name, y.Name) })
	return items
}

// FilterTags lists the tags of all (by name) that query fuzzy-matches,
// best first; all of them for an empty query.
func FilterTags(all []Tag, query string) []Tag {
	q := strings.TrimSpace(query)
	if q == "" {
		return all
	}
	names := make([]string, len(all))
	for i, t := range all {
		names[i] = t.Name
	}
	var out []Tag
	for _, m := range fuzzy.Find(q, names) {
		out = append(out, all[m.Index])
	}
	return out
}

// FindTags lists g's tags that query fuzzy-matches, best first; all of
// them, by name, for an empty query.
func FindTags(g *project.Graph, query string) []Tag { return FilterTags(Tags(g), query) }

// ProjectMatches reports whether every word of filter is in the project's
// tool or path, whatever their case.
func ProjectMatches(filter string, it session.Project) bool {
	hay := strings.ToLower(it.Backend + " " + it.Root)
	for _, t := range strings.Fields(strings.ToLower(filter)) {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

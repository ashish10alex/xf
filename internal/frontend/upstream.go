package frontend

import (
	"github.com/ashish10alex/xf/internal/project"
)

// The dependency inspector (ADR 0033) lists a model and the tables
// upstream of it, to filter and query each one.

// MaxUpstreamDepth bounds how many levels Upstream walks.
const MaxUpstreamDepth = 20

// UpstreamNode is the model, or one of the actions it reads, directly or
// through others.
type UpstreamNode struct {
	ID   project.ActionID `json:"id"`
	Name string           `json:"name"`
	Kind string           `json:"kind"`
	// Depth is 0 for the model, 1 for what it reads, and so on: the
	// shortest way there; AddedDepth for a table added by hand.
	Depth int `json:"depth"`
	// Table says the action has a table to query. One without (a dbt
	// ephemeral model, an operation with no output) gets no row; the walk
	// goes on through it.
	Table bool `json:"table"`
	// Key is the action's name before dev settings apply (see PresetKey).
	Key string `json:"key"`
	// File is the action's file, "" for a declaration or a target no
	// action defines.
	File string `json:"file,omitempty"`
}

// UpstreamEdge says From reads To.
type UpstreamEdge struct {
	From project.ActionID `json:"from"`
	To   project.ActionID `json:"to"`
}

// Upstream walks g from model to what it reads, breadth first, depth
// levels deep (1 to MaxUpstreamDepth), leaving out assertions and tests.
// The nodes come in walk order, the model first; nil when g has no model.
func Upstream(g *project.Graph, model project.ActionID, depth int) ([]UpstreamNode, []UpstreamEdge) {
	m := g.Actions[model]
	if m == nil {
		return nil, nil
	}
	depth = min(max(depth, 1), MaxUpstreamDepth)
	nodes := []UpstreamNode{nodeOf(m, 0)}
	seen := map[project.ActionID]bool{model: true}
	var edges []UpstreamEdge
	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		if n.Depth >= depth {
			continue
		}
		a := g.Actions[n.ID]
		if a == nil {
			continue
		}
		for _, t := range a.DependencyTargets {
			id := t.ID()
			dep := g.Actions[id]
			if dep == nil {
				dep = &project.Action{ID: id, Target: t, CanonicalTarget: t, Kind: project.KindUnknown}
			}
			if dep.Kind == project.KindAssertion || dep.Kind == project.KindTest {
				continue
			}
			edges = append(edges, UpstreamEdge{From: n.ID, To: id})
			if seen[id] {
				continue
			}
			seen[id] = true
			nodes = append(nodes, nodeOf(dep, n.Depth+1))
		}
	}
	return nodes, edges
}

// AddedDepth is the depth of a table added to the inspector by hand,
// which needn't be upstream of the model at all.
const AddedDepth = -1

// AddedNode is the node of a table added by hand.
func AddedNode(a *project.Action) UpstreamNode { return nodeOf(a, AddedDepth) }

// ActionByPresetKey is g's action that key names (see PresetKey), nil for
// none: how a table added by hand is found again under other dev settings.
func ActionByPresetKey(g *project.Graph, key string) *project.Action {
	for _, a := range g.Actions {
		if a.Kind != project.KindAssertion && a.Kind != project.KindTest && PresetKey(a) == key {
			return a
		}
	}
	return nil
}

// ReaderOf is an action of g that reads id, the first by ID, nil for
// none: what an added table's read (defer to prod's) is taken from.
func ReaderOf(g *project.Graph, id project.ActionID) *project.Action {
	var out *project.Action
	for _, a := range g.Actions {
		if out != nil && a.ID >= out.ID {
			continue
		}
		for _, t := range a.DependencyTargets {
			if t.ID() == id {
				out = a
				break
			}
		}
	}
	return out
}

func nodeOf(a *project.Action, depth int) UpstreamNode {
	return UpstreamNode{ID: a.ID, Name: a.Target.Name, Kind: a.Kind.String(), Depth: depth,
		Table: QueryableTable(a), Key: PresetKey(a), File: a.FileName}
}

// QueryableTable says a has a table or view of its target's name to
// select from: an operation only with hasOutput, and a target no action
// defines is taken to be one.
func QueryableTable(a *project.Action) bool {
	switch a.Kind {
	case project.KindUnknown:
		return true
	case project.KindOperation:
		return a.HasOutput
	}
	return a.Kind.HasTable()
}

// PresetKey names a for the dependency inspector's saved filters, so
// they match whoever runs it and wherever: in dbt the node's name as
// ref() or source() gives it, in Dataform the canonical target's dataset
// and name (what ref() matches; defer to prod matches prod by the same),
// before a table prefix, schema suffix or target changes them.
func PresetKey(a *project.Action) string {
	if d := a.DbtName; d != nil {
		if d.Source != "" {
			return "source:" + d.Source + "." + d.Name
		}
		k := d.Name
		if d.Package != "" {
			k = d.Package + "." + k
		}
		if d.Version != "" {
			k += ".v" + d.Version
		}
		return k
	}
	t := a.CanonicalTarget
	if t.Name == "" {
		t = a.Target
	}
	return t.Schema + "." + t.Name
}

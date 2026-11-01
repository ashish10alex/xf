package impact

import (
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// A dbt target names the schemas, so the same project compiled with its
// prod target gives every model another ID than xf's compile, made with the
// target xf browses with. To compare with prod, the dbt check compiles the
// working tree with the prod target and works on that compile alone: it
// dry runs what prod would build, reading prod's tables, which exist where
// a dev target's often don't. Retarget names the Changed Actions as that
// compile does.

// retargetKey identifies a dbt node whatever the target: its kind and its
// fqn. It is "" for an action without one (sources, exposures, ephemeral
// models), which keeps its ID.
func retargetKey(a *project.Action) string {
	if a.RunName == "" {
		return ""
	}
	return a.Kind.String() + "\x00" + a.RunName
}

// Retarget names the actions of res, a diff worked out on g, as other
// names them: other is the same tree compiled with another target. Actions
// are matched by kind and fqn (project.Action.RunName); one other doesn't
// have keeps its ID. A deleted action is in neither compile, so its table
// is guessed from an action that shared its dataset in g. The map returned
// takes each of g's tables to other's, so readers that are runs of g's
// target fold into the actions (see Deps.ToProd).
func Retarget(res *changes.Result, g, other *project.Graph) (*changes.Result, map[string]string) {
	byKey := map[string]*project.Action{}
	for _, a := range other.Actions {
		if k := retargetKey(a); k != "" {
			byKey[k] = a
		}
	}
	toOther := map[string]string{}
	for _, a := range g.Actions {
		if k := retargetKey(a); k != "" {
			if o := byKey[k]; o != nil && o.ID != a.ID {
				toOther[string(a.ID)] = string(o.ID)
			}
		}
	}
	// [other, g] pairs, sorted, for guessing deleted tables.
	pairs := make([][2]string, 0, len(toOther))
	for from, to := range toOther {
		pairs = append(pairs, [2]string{to, from})
	}
	slices.SortFunc(pairs, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	deleted := map[string]string{}
	for _, d := range res.Deleted {
		if t := guessDevTarget(string(d.ID), pairs); t != string(d.ID) {
			deleted[string(d.ID)] = t
		}
	}
	id := func(x project.ActionID) project.ActionID {
		if x == "" {
			return x
		}
		if t, ok := toOther[string(x)]; ok {
			return project.ActionID(t)
		}
		if t, ok := deleted[string(x)]; ok {
			return project.ActionID(t)
		}
		return x
	}

	out := *res
	out.Changed = make([]changes.Changed, len(res.Changed))
	for i, c := range res.Changed {
		c.Reasons = slices.Clone(c.Reasons)
		if a := other.Actions[id(c.ID)]; a != nil {
			c.Target = a.Target
		}
		c.ID, c.Parent, c.RenamedFrom = id(c.ID), id(c.Parent), id(c.RenamedFrom)
		out.Changed[i] = c
	}
	out.Deleted = make([]changes.Deleted, len(res.Deleted))
	for i, d := range res.Deleted {
		d.ID, d.Parent, d.RenamedTo = id(d.ID), id(d.Parent), id(d.RenamedTo)
		out.Deleted[i] = d
	}
	for from, to := range deleted {
		toOther[from] = to
	}
	return &out, toOther
}

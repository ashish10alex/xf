package frontend

import "github.com/ashish10alex/xf/internal/project"

// ExternalsInFile are the actions the declarations table lists for a: the
// actions of a's file when there are two or more and all are external
// (Dataform declarations, dbt sources), the tests a sources.yml also
// defines left out. Nil for any other file, whose actions are listed as
// project.Graph.Siblings lists them (ADR 0015).
func ExternalsInFile(g *project.Graph, a *project.Action) []*project.Action {
	if g == nil || a == nil || !a.Kind.External() || a.FileName == "" {
		return nil
	}
	var out []*project.Action
	for _, b := range g.ActionsInFile(a.FileName) {
		switch {
		case b.Kind == project.KindTest:
		case !b.Kind.External():
			return nil
		default:
			out = append(out, b)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

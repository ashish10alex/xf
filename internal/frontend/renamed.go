package frontend

import (
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// RenamedAction is the action file defines now in place of id, which the
// graph g no longer has: an action is named by its target, so a change to
// its config's schema, database or name renames it. It is the file's
// action of id's name, else the file's first; nil when id is still in g,
// or file defines no action.
func RenamedAction(g *project.Graph, id project.ActionID, file string) *project.Action {
	if g == nil || file == "" {
		return nil
	}
	if a := g.Actions[id]; a != nil && a.Kind != project.KindUnknown {
		return nil
	}
	in := g.ActionsInFile(file)
	if len(in) == 0 {
		return nil
	}
	name := string(id)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	for _, a := range in {
		if a.Target.Name == name {
			return a
		}
	}
	return in[0]
}

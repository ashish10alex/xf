package dbt

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/ashish10alex/xf/internal/project"
)

// Defer to prod in a dbt project (ADR 0031): dbt does it itself. Given the
// manifest of prod (a parse of the merge-base with dbtProdTarget), a ref()
// to a model that isn't selected and isn't built in the target resolves to
// where that manifest says it is. xf gives dbt the manifest, on its own
// compiles of the project and on runs, and reads back from the compiled SQL
// what dbt decided, never deciding itself.

// deferState is where --defer-state reads prod's manifest while defer to
// prod is on: "" while it is off.
type deferState struct {
	mu  sync.Mutex
	dir string
}

// SetDeferState makes the backend's compiles of the project defer to the
// manifest in dir, or with "" stop deferring. Only compiles of the project
// itself defer: not snapshots of other commits.
func (b *Backend) SetDeferState(dir string) {
	b.deferred.mu.Lock()
	defer b.deferred.mu.Unlock()
	b.deferred.dir = dir
}

// DeferState is the directory the backend's compiles defer to, "" for none.
func (b *Backend) DeferState() string {
	if b.deferred == nil {
		return ""
	}
	b.deferred.mu.Lock()
	defer b.deferred.mu.Unlock()
	return b.deferred.dir
}

// DeferArgs are the arguments that make a compile or a run defer to the
// manifest in dir; none for "".
func DeferArgs(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{"--defer", "--defer-state", dir}
}

// WriteDeferState puts manifest, prod's, where --defer-state reads it, and
// returns that directory.
func (b *Backend) WriteDeferState(root string, manifest []byte) (string, error) {
	return writeState(filepath.Join(b.variantDir(root), "defer-state"), manifest)
}

// ParseManifest builds the graph of a manifest dbt wrote, prod's that a
// compile defers to.
func ParseManifest(data []byte) (*project.Graph, error) {
	m, err := parseManifest(data)
	if err != nil {
		return nil, err
	}
	return m.graph()
}

// relationOf is how BigQuery SQL that dbt compiled names a table.
func relationOf(t project.Target) string {
	return "`" + t.Database + "`.`" + t.Schema + "`.`" + t.Name + "`"
}

// DeferRead is an upstream table of what a compile with --defer shows,
// and what it was read from.
type DeferRead struct {
	Dev project.Target
	// Prod is set when it was read from prod; nil when from the target.
	Prod *project.Target
}

// Deferred says which upstream tables of the actions shown dev, a compile
// with --defer, read from prod, a parse of the manifest it deferred to.
// An action is the same in both when dbt calls it the same (RunName), as
// its target differs. A table is listed when prod has it somewhere else
// than the target: from prod when the SQL of an action shown names its
// table in prod, from dev when it names its table in the target. One the
// SQL names in neither place isn't read (a ref() in a branch of Jinja not
// taken). Sources, what prod doesn't have and an action shown itself are
// left out. An ephemeral model has no table: what it reads counts as read
// by the action it is inlined into.
func Deferred(dev, prod *project.Graph, shown []project.ActionID) []DeferRead {
	prodOf := map[string]project.Target{}
	for _, a := range prod.Actions {
		if a.RunName != "" && a.Kind.HasTable() && !a.Kind.External() {
			prodOf[a.RunName+"\x00"+a.Kind.String()] = a.Target
		}
	}
	listed := map[project.ActionID]bool{}
	for _, id := range shown {
		listed[id] = true
	}
	var out []DeferRead
	for _, id := range shown {
		a := dev.Actions[id]
		if a == nil || a.SQL != project.SQLCompiled {
			continue
		}
		var parts []string
		for _, q := range a.DryRunQueries() {
			parts = append(parts, q.SQL)
		}
		sql := strings.Join(append(parts, a.Query), "\n")
		visited := map[project.ActionID]bool{}
		queue := slices.Clone(a.DependencyTargets)
		for len(queue) > 0 {
			t := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			up := dev.Actions[t.ID()]
			if up == nil || visited[up.ID] {
				continue
			}
			visited[up.ID] = true
			if up.Kind == project.KindEphemeral {
				queue = append(queue, up.DependencyTargets...)
				continue
			}
			p, ok := prodOf[up.RunName+"\x00"+up.Kind.String()]
			if !ok || up.Kind.External() || !up.Kind.HasTable() || p == up.Target || listed[up.ID] {
				continue
			}
			switch {
			case strings.Contains(sql, relationOf(p)):
				listed[up.ID] = true
				out = append(out, DeferRead{Dev: up.Target, Prod: &p})
			case strings.Contains(sql, relationOf(up.Target)):
				listed[up.ID] = true
				out = append(out, DeferRead{Dev: up.Target})
			}
		}
	}
	slices.SortFunc(out, func(a, b DeferRead) int { return strings.Compare(string(a.Dev.ID()), string(b.Dev.ID())) })
	return out
}

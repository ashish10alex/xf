package impact

import (
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// Dataplex records lineage from every run, dev runs included, so a reader
// may be a dev run of an action in this project: the extension folds it
// into the action's prod table rather than show it as a reader outside the
// project. It knows the dev targets from its own compile, made with the
// user's compiler options; xf compiles with none, so it compiles once more
// with devCompilerOptions to find them. With prodCompilerOptions it compiles
// with those too, the same way: prod runs fold into their actions as dev
// runs do, and the map the other way round is each action's prod table.

// mapped reports the kinds of action a dev target is mapped for: what the
// extension's buildProdTargetMap indexes (tables, operations, assertions,
// declarations).
func mapped(a *project.Action) bool {
	switch a.Kind {
	case project.KindNotebook, project.KindPropertyGraph, project.KindUnknown:
		return false
	}
	return true
}

// shortKey is a target's schema.name, as the extension's prodKey.
func shortKey(t project.Target) string { return t.Schema + "." + t.Name }

// DevToProd maps the targets of dev, the project compiled with the dev
// compiler options, to those of prod, the project compiled as xf compiles
// it, matching actions by canonical target: with the database when it
// matches, else by schema and name when only one prod action has them. The
// actions the branch deletes aren't in either compile; their dev targets
// are guessed from an action that shares their prod dataset.
func DevToProd(dev, prod *project.Graph, deleted []changes.Deleted) map[string]string {
	full := map[project.ActionID]project.ActionID{}
	short := map[string]project.ActionID{}
	ambiguous := map[string]bool{}
	for _, a := range prod.Actions {
		if !mapped(a) {
			continue
		}
		full[a.CanonicalTarget.ID()] = a.ID
		key := shortKey(a.CanonicalTarget)
		if existing, ok := short[key]; ok && existing != a.ID {
			ambiguous[key] = true
		}
		short[key] = a.ID
	}
	out := map[string]string{}
	for _, a := range dev.Actions {
		if !mapped(a) {
			continue
		}
		p, ok := full[a.CanonicalTarget.ID()]
		if !ok && !ambiguous[shortKey(a.CanonicalTarget)] {
			p, ok = short[shortKey(a.CanonicalTarget)]
		}
		if ok {
			out[string(a.ID)] = string(p)
		}
	}
	pairs := make([][2]string, 0, len(out))
	for d, p := range out {
		pairs = append(pairs, [2]string{d, p})
	}
	slices.SortFunc(pairs, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	for _, d := range deleted {
		if devID := guessDevTarget(string(d.ID), pairs); devID != string(d.ID) {
			if _, taken := out[devID]; !taken {
				out[devID] = string(d.ID)
			}
		}
	}
	return out
}

// guessDevTarget is the dev target of deleted, a prod table, from an action
// that shared its prod dataset: the extension's guessProdTarget the other
// way round, since xf knows deleted actions by their prod table.
func guessDevTarget(deleted string, devToProd [][2]string) string {
	parts := strings.Split(deleted, ".")
	if len(parts) != 3 {
		return deleted
	}
	for _, pair := range devToProd {
		dev, prod := strings.Split(pair[0], "."), strings.Split(pair[1], ".")
		if len(dev) == 3 && len(prod) == 3 && prod[0] == parts[0] && prod[1] == parts[1] {
			return dev[0] + "." + dev[1] + "." + parts[2]
		}
	}
	return deleted
}

// ProdTargetFunc is the prod table of an action, or of one the branch
// deletes, from m, DevToProd of a compile with the prod compiler options:
// that map the other way round. It is "" for a table m has no prod table
// of.
func ProdTargetFunc(m map[string]string) func(string) string {
	targets := map[string]string{}
	for prod, action := range m {
		if t, ok := targets[action]; !ok || prod < t {
			targets[action] = prod
		}
	}
	return func(table string) string { return targets[table] }
}

// ToProdFunc folds a reader into its prod table with m, from DevToProd;
// nil when m is empty.
func ToProdFunc(m map[string]string) func(string) string {
	if len(m) == 0 {
		return nil
	}
	return func(table string) string {
		if p, ok := m[table]; ok {
			return p
		}
		return table
	}
}

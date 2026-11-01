package frontend

import (
	"fmt"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/project"
)

// DeferHint is how defer to prod (ADR 0031) labels an upstream table: the
// label after its ref(), the line in pane 2's list, and what a hover says.
type DeferHint struct {
	Dev string `json:"dev"`
	// Prod is the table read in its place, "" when none is.
	Prod  string `json:"prod,omitempty"`
	Label string `json:"label"`
	// Tone is prod (read from prod), warn or dev (read from dev).
	Tone  string `json:"tone"`
	Hover string `json:"hover"`
	// URL opens the table read, prod's when it is, in the BigQuery console.
	URL string `json:"url,omitempty"`
}

// DeferHints are the hints of a's upstream tables under d (d.For(a)), by
// the dependency's ID, in dependency order: the tables read from prod or
// that can't be, those built in dev, and declarations, which read the
// same table in dev and prod. now ages the dev tables.
func DeferHints(g *project.Graph, a *project.Action, d *deferral.Deferral, now time.Time) ([]project.ActionID, map[project.ActionID]DeferHint) {
	hints := map[project.ActionID]DeferHint{}
	if d != nil {
		for _, e := range d.Entries {
			hints[e.Dev.ID()] = entryHint(e)
		}
		for _, b := range d.BuiltInDev {
			hints[b.Dev.ID()] = builtInDevHint(b, now)
		}
	}
	var order []project.ActionID
	for _, t := range a.DependencyTargets {
		id := t.ID()
		if _, ok := hints[id]; !ok {
			if dep := g.Actions[id]; dep != nil && dep.Kind.External() {
				hints[id] = DeferHint{Dev: string(id), Label: "✓ declaration", Tone: "dev", URL: t.ConsoleURL(),
					Hover: "Defer to prod: a declaration, which the project doesn't build, so dev and prod read the same table.\n" + string(id)}
			} else {
				continue
			}
		}
		order = append(order, id)
	}
	return order, hints
}

func entryHint(e deferral.Entry) DeferHint {
	h := DeferHint{Dev: string(e.Dev.ID())}
	route := string(e.Dev.ID())
	if e.Prod != nil {
		h.Prod = string(e.Prod.ID())
		route += "\n→ " + h.Prod
	}
	switch {
	case e.Status == deferral.Deferred && e.Stale:
		h.Label, h.Tone = "⚠ prod · changed on this branch", "warn"
		h.Hover = "Defer to prod: read from prod, but changed on this branch, so prod may be out of date.\n" + route
	case e.Status == deferral.Deferred:
		h.Label, h.Tone = "→ prod", "prod"
		h.Hover = "Defer to prod: not built in dev, so read from prod.\n" + route
	case e.Status == deferral.Unreadable:
		h.Label, h.Tone = "⚠ no prod access", "warn"
		h.Hover = "Defer to prod: not built in dev, and no read access to prod.\n" + route
	default:
		h.Label, h.Tone = "⚠ not built in dev or prod", "warn"
		h.Hover = "Defer to prod: not built in dev or prod.\n" + route
	}
	if e.Prod != nil && e.Status == deferral.Deferred {
		h.URL = e.Prod.ConsoleURL()
	}
	return h
}

func builtInDevHint(b deferral.BuiltInDev, now time.Time) DeferHint {
	h := DeferHint{Dev: string(b.Dev.ID()), Label: "✓ dev · built in dev", Tone: "dev", URL: b.Dev.ConsoleURL(),
		Hover: "Defer to prod: built in dev, so read from dev.\n" + string(b.Dev.ID())}
	if b.LastModified > 0 {
		t := time.UnixMilli(b.LastModified)
		h.Label = "✓ dev · built " + RelTime(now, t)
		h.Hover += "\nLast updated " + t.Local().Format("2006-01-02 15:04")
	}
	return h
}

// DeferRunLine says what a deferred run reads from prod, for the run
// preview and the top of its log: "reads 2 upstream tables from prod".
func DeferRunLine(d *deferral.Deferral) string {
	n := d.Deferred()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("reads %d upstream %s from prod", n, plural(n, "table", "tables"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// DeferredPairs are the tables d reads from prod, as "dev → prod", for
// the run history.
func DeferredPairs(d *deferral.Deferral) []string {
	if d == nil {
		return nil
	}
	var out []string
	for _, e := range d.Entries {
		if e.Read() {
			out = append(out, string(e.Dev.ID())+" → "+string(e.Prod.ID()))
		}
	}
	return out
}

// DeferBlocked words the entries a run can't read from prod.
func DeferBlocked(d *deferral.Deferral, missingDatasets map[string]bool) []string {
	var out []string
	for _, e := range d.Entries {
		switch {
		case e.Status == deferral.MissingEverywhere:
			out = append(out, string(e.Dev.ID())+": not built in dev or prod")
		case e.Status == deferral.Unreadable:
			out = append(out, string(e.Dev.ID())+": not built in dev, and no read access to prod")
		case e.Read() && !e.Routine && missingDatasets[e.Dev.Database+"."+e.Dev.Schema]:
			out = append(out, fmt.Sprintf("%s: the dev dataset %s.%s doesn't exist", e.Dev.ID(), e.Dev.Database, e.Dev.Schema))
		}
	}
	return out
}

// DeferProdDatasets are the prod datasets d reads, which a run's account
// needs to read.
func DeferProdDatasets(d *deferral.Deferral) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range d.Entries {
		if e.Read() {
			ds := e.Prod.Database + "." + e.Prod.Schema
			if !seen[ds] {
				seen[ds] = true
				out = append(out, ds)
			}
		}
	}
	return out
}

// DeferStale words the Stale Deferrals of d.
func DeferStale(d *deferral.Deferral) []string {
	var out []string
	for _, e := range d.Entries {
		if e.Read() && e.Stale {
			out = append(out, string(e.Dev.ID())+" reads "+string(e.Prod.ID()))
		}
	}
	return out
}

// TargetNames joins targets' IDs one per line.
func TargetNames(ts []project.Target) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = string(t.ID())
	}
	return strings.Join(names, "\n")
}

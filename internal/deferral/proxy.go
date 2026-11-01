package deferral

import (
	"context"
	"fmt"
	"strings"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// Writer makes, repoints and removes Proxy Views; *bigquery.Client is one.
type Writer interface {
	View(ctx context.Context, project, dataset, table string) (*bigquery.ViewInfo, error)
	CreateView(ctx context.Context, v bigquery.ViewSpec) error
	UpdateView(ctx context.Context, v bigquery.ViewSpec) error
	DeleteTable(ctx context.Context, project, dataset, table string) error
	ListTablesLabelled(ctx context.Context, project, dataset string) ([]bigquery.LabelledTable, error)
}

// ProxyResult says what EnsureProxies did.
type ProxyResult struct {
	Created, Updated, Kept int
}

func (r ProxyResult) String() string {
	var parts []string
	if r.Created > 0 {
		parts = append(parts, fmt.Sprintf("made %d", r.Created))
	}
	if r.Updated > 0 {
		parts = append(parts, fmt.Sprintf("repointed %d", r.Updated))
	}
	if len(parts) == 0 {
		return ""
	}
	n := r.Created + r.Updated
	return strings.Join(parts, ", ") + " proxy " + plural(n, "view", "views")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// EnsureProxies makes a Proxy View at the Dev Target of each deferred
// entry, reading its Prod Target, before a run, so Dataform's own runner
// reads prod through dev names. One already reading the right table is
// kept; a real table or view is never touched (it is built in dev, so it
// isn't deferred, unless it was built since the Deferral was worked out).
func EnsureProxies(ctx context.Context, w Writer, entries []Entry) (ProxyResult, error) {
	var r ProxyResult
	for _, e := range entries {
		if !e.Read() || e.Routine {
			continue
		}
		existing, err := w.View(ctx, e.Dev.Database, e.Dev.Schema, e.Dev.Name)
		if err != nil {
			return r, fmt.Errorf("%s: %w", e.Dev.ID(), err)
		}
		var labels map[string]string
		var query string
		if existing != nil {
			labels, query = existing.Labels, existing.Query
		}
		spec := bigquery.ViewSpec{Project: e.Dev.Database, Dataset: e.Dev.Schema, Table: e.Dev.Name, Query: ProxyQuery(*e.Prod),
			Labels: map[string]string{ProxyLabel: "true"}, Description: ProxyDescription(*e.Prod)}
		switch StepFor(labels, query, existing != nil, *e.Prod) {
		case ProxyCreate:
			err = w.CreateView(ctx, spec)
			r.Created++
		case ProxyUpdate:
			err = w.UpdateView(ctx, spec)
			r.Updated++
		case ProxyKeep:
			r.Kept++
		}
		if err != nil {
			return r, fmt.Errorf("proxy view %s: %w", e.Dev.ID(), err)
		}
	}
	return r, nil
}

// RemoveProxies removes the Proxy Views in the datasets of g's actions,
// and only those: a view without the label is never touched. It returns
// the views it removed; with remove false it only finds them.
func RemoveProxies(ctx context.Context, w Writer, g *project.Graph, remove bool) ([]project.Target, error) {
	datasets := map[[2]string]bool{}
	for _, a := range g.Actions {
		if a.Kind.External() || a.Kind == project.KindUnknown || a.Target.Database == "" {
			continue
		}
		datasets[[2]string{a.Target.Database, a.Target.Schema}] = true
	}
	var removed []project.Target
	for ds := range datasets {
		tables, err := w.ListTablesLabelled(ctx, ds[0], ds[1])
		if bigquery.IsNotFound(err) {
			continue
		}
		if err != nil {
			return removed, fmt.Errorf("listing %s.%s: %w", ds[0], ds[1], err)
		}
		for _, t := range tables {
			if t.Labels[ProxyLabel] != "true" {
				continue
			}
			if !remove {
				removed = append(removed, project.Target{Database: ds[0], Schema: ds[1], Name: t.ID})
				continue
			}
			if err := w.DeleteTable(ctx, ds[0], ds[1], t.ID); err != nil {
				return removed, fmt.Errorf("removing %s.%s.%s: %w", ds[0], ds[1], t.ID, err)
			}
			removed = append(removed, project.Target{Database: ds[0], Schema: ds[1], Name: t.ID})
		}
	}
	return removed, nil
}

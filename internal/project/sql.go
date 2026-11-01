package project

import (
	"fmt"
	"strings"
)

// SQLSection is one titled block of an action's compiled SQL.
type SQLSection struct {
	Title string
	SQL   string
}

// HasIncrementalVariant reports whether the action has SQL for runs that
// update an existing table as well as for full rebuilds: a Dataform
// incremental table does; a dbt one is compiled once, for one or the other.
func (a *Action) HasIncrementalVariant() bool {
	return a.Kind == KindIncremental && (a.IncrementalQuery != "" || len(a.IncrementalPreOps) > 0)
}

// SQLSections returns the SQL the action executes, in execution order. For
// incremental tables, incremental selects the incremental variant (the run of
// an existing table) instead of the full rebuild. Empty sections are skipped.
func (a *Action) SQLSections(incremental bool) []SQLSection {
	var out []SQLSection
	addOps := func(title string, ops []string) {
		for i, op := range ops {
			t := title
			if len(ops) > 1 {
				t = fmt.Sprintf("%s %d/%d", title, i+1, len(ops))
			}
			out = appendSection(out, t, op)
		}
	}

	switch {
	case a.HasIncrementalVariant() && incremental:
		addOps("incremental pre_operations", a.IncrementalPreOps)
		out = appendSection(out, "incremental query", a.IncrementalQuery)
		post := a.IncrementalPostOps
		if len(post) == 0 {
			post = a.PostOps
		}
		addOps("incremental post_operations", post)
	case a.Kind == KindOperation:
		addOps("operation", a.Queries)
	case a.Kind == KindNotebook:
		out = appendSection(out, "notebook", a.NotebookContents)
	case a.Kind == KindPropertyGraph:
		out = appendSection(out, "property graph", a.GraphBody)
	default:
		addOps("pre_operations", a.PreOps)
		out = appendSection(out, "query", a.Query)
		addOps("post_operations", a.PostOps)
	}
	return out
}

func appendSection(out []SQLSection, title, sql string) []SQLSection {
	sql = strings.Trim(sql, "\n")
	if strings.TrimSpace(sql) == "" {
		return out
	}
	return append(out, SQLSection{Title: title, SQL: sql})
}

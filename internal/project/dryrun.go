package project

import (
	"slices"
	"strings"
	"unicode"
)

// DryRunQuery is a script to dry-run for an action: what BigQuery would plan
// when Dataform runs it.
type DryRunQuery struct {
	// Label tells an incremental table's two variants apart ("full",
	// "incremental"); it is empty for every other action.
	Label string
	SQL   string
	// PreOps and Query are, when SQL is a query after its
	// pre_operations, those statements and the query: SQL is Script of
	// them. Both are empty for every other script.
	PreOps []string
	Query  string
}

// DryRunQueries returns what to dry-run for the action, following the VS
// Code extension: a table's or view's query after its pre_operations (so
// variables they declare resolve), both variants of an incremental table, an
// assertion's query and an operation's statements. post_operations are left
// out, as the extension leaves them out of the cost. Declarations, notebooks
// and property graphs have nothing to dry-run.
//
// For dbt: a model's, snapshot's or test's compiled query, without its
// hooks, which are uncompiled (RawOps). An incremental model has one,
// compiled as dbt chose. Seeds, sources, exposures, analyses, ephemeral
// models and unit tests have nothing to dry-run, nor does an action that
// can't run (NoRun: on-run hooks, disabled nodes).
func (a *Action) DryRunQueries() []DryRunQuery {
	if a.NoRun {
		return nil
	}
	var out []DryRunQuery
	add := func(label string, stmts ...string) {
		if sql := script(stmts); sql != "" {
			out = append(out, DryRunQuery{Label: label, SQL: sql})
		}
	}
	// addQuery adds query after the statements of pre.
	addQuery := func(label string, pre []string, query string) {
		n := len(out)
		add(label, append(slices.Clone(pre), query)...)
		if len(out) > n && slices.ContainsFunc(pre, func(s string) bool { return strings.TrimSpace(s) != "" }) {
			out[n].PreOps, out[n].Query = slices.Clone(pre), query
		}
	}
	pre := a.PreOps
	if a.RawOps {
		pre = nil
	}
	switch a.Kind {
	case KindTable, KindView, KindMaterializedView:
		addQuery("", pre, a.Query)
	case KindIncremental:
		if !a.HasIncrementalVariant() {
			addQuery("", pre, a.Query)
			break
		}
		addQuery("full", pre, a.Query)
		addQuery("incremental", a.IncrementalPreOps, a.IncrementalQuery)
	case KindAssertion, KindSnapshot, KindTest:
		add("", a.Query)
	case KindOperation:
		add("", a.Queries...)
	}
	return out
}

// DryRunTarget is where to dry run a's queries: the project's
// defaultProject, else the action's own project. location is "", so
// BigQuery runs the job where the tables the query reads are. It is not
// defaultLocation: Dataform uses that only to make new datasets, and runs
// its jobs in the location of .df-credentials.json. A job in another
// location than a table it reads fails with "Access Denied … or perhaps it
// does not exist", even when the user can read the table.
func (g *Graph) DryRunTarget(a *Action) (project, location string) {
	project = g.DefaultDatabase
	if project == "" {
		project = a.Target.Database
	}
	return project, ""
}

// ScriptPart places one statement of a Script.
type ScriptPart struct {
	// Stmt is the statement's index in what Script was given.
	Stmt int
	// Start is where the statement begins in the script. Origin is where its
	// untrimmed text would begin, so an offset o in the part is o-Origin in
	// the statement as given.
	Start, Origin int
}

func script(stmts []string) string {
	sql, _ := Script(stmts)
	return sql
}

// Script joins statements into one BigQuery script, each ending in a
// semicolon, one per line; empty ones are left out. A single statement is
// returned as it is, trimmed. Everything that dry-runs an action builds its
// text here, so the same action always sends the same SQL.
func Script(stmts []string) (string, []ScriptPart) {
	type kept struct {
		i    int
		text string
		lead int
	}
	var parts []kept
	for i, s := range stmts {
		t := strings.TrimSpace(s)
		if t == "" {
			continue
		}
		parts = append(parts, kept{i, t, len(s) - len(strings.TrimLeftFunc(s, unicode.IsSpace))})
	}
	var b strings.Builder
	var out []ScriptPart
	for j, p := range parts {
		if j > 0 {
			b.WriteString("\n")
		}
		out = append(out, ScriptPart{Stmt: p.i, Start: b.Len(), Origin: b.Len() - p.lead})
		b.WriteString(p.text)
		if len(parts) > 1 && !strings.HasSuffix(p.text, ";") {
			b.WriteString(";")
		}
	}
	return b.String(), out
}

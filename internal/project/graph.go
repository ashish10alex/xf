// Package project models a compiled project: its actions, their targets in
// the warehouse and the dependencies between them. It knows no tool; each
// backend (Dataform, dbt) builds a Graph from its own compile output.
package project

import (
	"cmp"
	"fmt"
	"slices"
)

// Target is where an action's output lives in the warehouse.
type Target struct {
	Database string
	Schema   string
	Name     string
}

// ID identifies an action by its fully qualified target. It matches the
// Dataform CLI's targetAsReadableString, so it can be passed to --actions;
// for dbt it is the relation (database.schema.alias) a model builds.
func (t Target) ID() ActionID {
	if t.Database == "" {
		return ActionID(t.Schema + "." + t.Name)
	}
	return ActionID(t.Database + "." + t.Schema + "." + t.Name)
}

// ConsoleURL opens the table or view in the BigQuery console.
func (t Target) ConsoleURL() string {
	return fmt.Sprintf("https://console.cloud.google.com/bigquery?project=%s&ws=!1m5!1m4!4m3!1s%s!2s%s!3s%s",
		t.Database, t.Database, t.Schema, t.Name)
}

// ActionID is a fully qualified target: "database.schema.name" (or
// "schema.name" when the target has no database).
type ActionID string

// ActionKind is the type of an action. The order is the display order: a
// file's table, view or operation before the assertions generated from it.
// Each backend uses the kinds its tool has: Dataform never makes a seed, dbt
// never a notebook.
type ActionKind int

const (
	KindTable ActionKind = iota
	KindView
	KindIncremental
	KindMaterializedView
	// KindEphemeral is a dbt model that is never built: it is inlined as a
	// CTE into the models that use it.
	KindEphemeral
	KindSeed
	KindSnapshot
	KindOperation
	KindAssertion
	// KindTest is a dbt data test or unit test.
	KindTest
	// KindAnalysis is a dbt analysis: SQL that is compiled, never built.
	KindAnalysis
	KindNotebook
	KindPropertyGraph
	KindDeclaration
	// KindSource is a dbt source: a table loaded outside the project, as a
	// Dataform declaration is.
	KindSource
	// KindExposure is a dbt exposure: a dashboard or application
	// downstream of the project, with no table of its own.
	KindExposure
	// KindUnknown is a placeholder for a dependency target that no action or
	// declaration in the graph defines.
	KindUnknown
)

var kindNames = map[ActionKind][2]string{
	KindTable:            {"table", "tbl"},
	KindView:             {"view", "view"},
	KindIncremental:      {"incremental", "inc"},
	KindMaterializedView: {"materialized view", "mv"},
	KindEphemeral:        {"ephemeral", "eph"},
	KindSeed:             {"seed", "seed"},
	KindSnapshot:         {"snapshot", "snap"},
	KindOperation:        {"operation", "op"},
	KindAssertion:        {"assertion", "asrt"},
	KindTest:             {"test", "test"},
	KindAnalysis:         {"analysis", "anl"},
	KindNotebook:         {"notebook", "nb"},
	KindPropertyGraph:    {"property graph", "pg"},
	KindDeclaration:      {"declaration", "decl"},
	KindSource:           {"source", "src"},
	KindExposure:         {"exposure", "exp"},
	KindUnknown:          {"unknown", "?"},
}

func (k ActionKind) String() string { return kindNames[k][0] }

// Short is the abbreviated kind shown next to action names.
func (k ActionKind) Short() string { return kindNames[k][1] }

// External reports whether actions of the kind are defined outside the
// project: a Dataform declaration or a dbt source.
func (k ActionKind) External() bool { return k == KindDeclaration || k == KindSource }

// HasTable reports whether an action of the kind builds a table or view
// of its own name: an operation, notebook or property graph may not, and an
// ephemeral model, analysis, test or exposure never does.
func (k ActionKind) HasTable() bool {
	switch k {
	case KindOperation, KindNotebook, KindPropertyGraph, KindEphemeral, KindAnalysis, KindTest, KindExposure, KindUnknown:
		return false
	}
	return true
}

// Action is one node of the compiled graph.
type Action struct {
	ID     ActionID
	Target Target
	// CanonicalTarget is the target before namePrefix, schemaSuffix and the
	// like are applied: what ref() arguments are matched against. It equals
	// Target when compile output has none.
	CanonicalTarget Target
	Kind            ActionKind
	FileName        string
	Tags            []string
	Disabled        bool

	Query              string
	IncrementalQuery   string
	PreOps             []string
	PostOps            []string
	IncrementalPreOps  []string
	IncrementalPostOps []string
	Queries            []string // operations
	// HasOutput is set on an operation that creates the table it names.
	HasOutput        bool
	NotebookContents string
	GraphBody        string

	// Description, Columns and PartitionBy come from the action's config.
	Description string
	Columns     []ColumnDescription
	PartitionBy string

	DependencyTargets []Target
	// ParentAction is set on assertions Dataform generates from a table's
	// config (uniqueKey, nonNull, rowConditions), and on dbt tests of a
	// model.
	ParentAction *Target

	// RunName is what a run selects the action by when that isn't its ID:
	// dbt's fqn, "jaffle_shop.marts.orders". Empty for Dataform.
	RunName string
	// NoRun marks an action its tool can't run on its own, whatever its
	// kind: a dbt on-run hook, or a disabled dbt node. Neither is dry-run.
	NoRun bool
	// RawOps says PreOps and PostOps are templates the tool compiles only
	// when it runs them (dbt's pre- and post-hooks): shown, never dry-run.
	RawOps bool
	// DbtName is how dbt's Jinja names the action, for the resources it
	// can: nil for Dataform, whose ref() names targets.
	DbtName *DbtName
	// SQL says how far the action's SQL is compiled; SQLCompiled but in a
	// graph shown before its compile is done (see backend.Stager).
	SQL SQLState
}

// SQLState says how far an action's SQL is compiled.
type SQLState int

const (
	// SQLCompiled is SQL the compile the graph is of produced.
	SQLCompiled SQLState = iota
	// SQLRaw is the action's template: its compile hasn't arrived.
	SQLRaw
	// SQLOutdated is SQL an earlier compile produced; project files have
	// changed since, so the compile under way may replace it.
	SQLOutdated
)

// DbtName is how dbt's Jinja names a resource: ref('name'),
// ref('package', 'name') or ref('name', v=2) for a model, seed or snapshot,
// source('source', 'name') for a source.
type DbtName struct {
	Package string
	// Source is a source's source name; empty for the rest.
	Source string
	Name   string
	// Version is a versioned model's version, and Latest whether it is the
	// one a ref() without v= picks.
	Version string
	Latest  bool
}

// Macro is a dbt macro, or generic test, of the project or an installed
// package. Jinja calls the project's by name and a package's as
// package.name().
type Macro struct {
	Name, Package string
	// FileName is relative to the project root.
	FileName    string
	Description string
	Arguments   []MacroArgument
}

// MacroArgument is a macro argument documented in YAML.
type MacroArgument struct {
	Name, Type, Description string
}

// ColumnDescription documents one column in an action's config. Path has
// more than one element for a field nested in a RECORD.
type ColumnDescription struct {
	Path        []string
	Description string
}

// Runnable reports whether a run can execute the action.
func (a *Action) Runnable() bool {
	if a.NoRun {
		return false
	}
	switch a.Kind {
	case KindDeclaration, KindSource, KindExposure, KindEphemeral, KindAnalysis, KindUnknown:
		return false
	}
	return true
}

// CompilationError is one entry of graphErrors.compilationErrors.
type CompilationError struct {
	FileName string
	Message  string
	// Stack is the JavaScript stack Dataform reports. A syntax error's
	// starts with the failing file and line: "/tmp/…/definitions/a.sqlx:10".
	// For dbt it is the file and line dbt names, when it does.
	Stack string
}

// Group is the actions of one kind, sorted for display.
type Group struct {
	Kind    ActionKind
	Actions []*Action
}

// Graph is a compiled project.
type Graph struct {
	Actions     map[ActionID]*Action
	Errors      []CompilationError
	CoreVersion string
	// DefaultDatabase and DefaultLocation are a Dataform project's
	// defaultProject and defaultLocation from workflow_settings.yaml: the
	// project Dataform runs its BigQuery jobs in, and where. Empty for dbt.
	DefaultDatabase string
	DefaultLocation string
	// DefaultSchema, TablePrefix, SchemaSuffix and DatabaseSuffix are the
	// rest of a Dataform compile's projectConfig: the dataset it builds in
	// and what it adds to the names, from the workflow settings or the
	// compile's options. Empty for dbt.
	DefaultSchema, TablePrefix, SchemaSuffix, DatabaseSuffix string
	// Notice is something about this compile the user should know, such as
	// that its SQL is uncompiled; empty for most.
	Notice string
	// Package is a dbt project's own package name, and Macros the macros
	// of it and its installed packages (not dbt's built-in ones), sorted by
	// package then name. Both are empty for Dataform.
	Package string
	Macros  []Macro
	// JSON is the compile result the graph was parsed from, so it can be
	// sent to another process and parsed there (see backend.Backend.Parse).
	JSON []byte

	deps       map[ActionID][]ActionID
	dependents map[ActionID][]ActionID
}

// NewGraph builds a graph from a backend's actions. The first action with
// an ID wins. A dependency target no action defines gets a KindUnknown
// placeholder. Dependencies and dependents are sorted for display.
func NewGraph(actions []*Action) *Graph {
	g := &Graph{
		Actions:    map[ActionID]*Action{},
		deps:       map[ActionID][]ActionID{},
		dependents: map[ActionID][]ActionID{},
	}
	for _, a := range actions {
		if _, ok := g.Actions[a.ID]; !ok {
			g.Actions[a.ID] = a
		}
	}
	for _, a := range g.actionsSnapshot() {
		for _, t := range a.DependencyTargets {
			dep := t.ID()
			if _, ok := g.Actions[dep]; !ok {
				g.Actions[dep] = &Action{ID: dep, Target: t, Kind: KindUnknown}
			}
			if !slices.Contains(g.deps[a.ID], dep) {
				g.deps[a.ID] = append(g.deps[a.ID], dep)
				g.dependents[dep] = append(g.dependents[dep], a.ID)
			}
		}
	}
	for _, m := range []map[ActionID][]ActionID{g.deps, g.dependents} {
		for id := range m {
			slices.SortFunc(m[id], func(x, y ActionID) int { return compareActions(g.Actions[x], g.Actions[y]) })
		}
	}
	return g
}

func (g *Graph) actionsSnapshot() []*Action {
	out := make([]*Action, 0, len(g.Actions))
	for _, a := range g.Actions {
		out = append(out, a)
	}
	return out
}

// compareActions orders by schema.name, then database.
func compareActions(a, b *Action) int {
	return cmp.Or(
		cmp.Compare(a.Target.Schema, b.Target.Schema),
		cmp.Compare(a.Target.Name, b.Target.Name),
		cmp.Compare(a.Target.Database, b.Target.Database),
	)
}

// Dependencies returns the actions the given action reads from.
func (g *Graph) Dependencies(id ActionID) []*Action { return g.lookup(g.deps[id]) }

// Dependents returns the actions that read from the given action.
func (g *Graph) Dependents(id ActionID) []*Action { return g.lookup(g.dependents[id]) }

func (g *Graph) lookup(ids []ActionID) []*Action {
	out := make([]*Action, 0, len(ids))
	for _, id := range ids {
		out = append(out, g.Actions[id])
	}
	return out
}

// Grouped returns the defined actions grouped by kind in display order, each
// group sorted by schema.name. Empty groups and unknown placeholders are left
// out.
func (g *Graph) Grouped() []Group {
	byKind := map[ActionKind][]*Action{}
	for _, a := range g.Actions {
		if a.Kind != KindUnknown {
			byKind[a.Kind] = append(byKind[a.Kind], a)
		}
	}
	var groups []Group
	for k := KindTable; k < KindUnknown; k++ {
		if len(byKind[k]) == 0 {
			continue
		}
		slices.SortFunc(byKind[k], compareActions)
		groups = append(groups, Group{Kind: k, Actions: byKind[k]})
	}
	return groups
}

// HasErrors reports whether the compile returned compilation errors.
func (g *Graph) HasErrors() bool { return len(g.Errors) > 0 }

// Siblings returns what is shown together with a: the actions defined in
// its file and the tests that read any of them (a dbt model's generic,
// singular and unit tests, which live in other files), sorted as
// ActionsInFile sorts. A test is shown with its HomeAction when it has
// one. An external action (a declaration or source) is shown alone, with
// the tests whose home it is, and is never another action's sibling: one
// declarations.js or sources.yml can define dozens. An action with no file
// is alone.
func (g *Graph) Siblings(a *Action) []*Action {
	if a.Kind == KindTest {
		if h := g.HomeAction(a); h != nil {
			a = h
		} else if a.FileName != "" {
			return g.ownActionsInFile(a.FileName)
		}
	}
	sortActions := func(out []*Action) {
		slices.SortFunc(out, func(x, y *Action) int {
			return cmp.Or(cmp.Compare(x.Kind, y.Kind), compareActions(x, y))
		})
	}
	if a.Kind.External() {
		var tests []*Action
		for _, t := range g.Dependents(a.ID) {
			if t.Kind == KindTest && g.HomeAction(t) == a {
				tests = append(tests, t)
			}
		}
		sortActions(tests)
		return append([]*Action{a}, tests...) // the source before its tests
	}
	if a.FileName == "" {
		return []*Action{a}
	}
	out := g.ownActionsInFile(a.FileName)
	for _, b := range out {
		if b.Kind == KindTest {
			continue
		}
		for _, t := range g.Dependents(b.ID) {
			if t.Kind == KindTest && !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	sortActions(out)
	return out
}

// ownActionsInFile is ActionsInFile without the external actions.
func (g *Graph) ownActionsInFile(fileName string) []*Action {
	return slices.DeleteFunc(g.ActionsInFile(fileName), func(a *Action) bool { return a.Kind.External() })
}

// HomeAction is the action a dbt test is shown with: the one it is
// attached to (ParentAction), else the only one it reads that the
// project builds, else the only source it reads (a source's tests);
// nil when there is no single one, or a isn't a test.
func (g *Graph) HomeAction(a *Action) *Action {
	if a.Kind != KindTest {
		return nil
	}
	if a.ParentAction != nil {
		if h := g.Actions[a.ParentAction.ID()]; h != nil && h.Kind != KindUnknown {
			return h
		}
	}
	var home, external *Action
	externals := 0
	for _, d := range g.Dependencies(a.ID) {
		switch {
		case d.Kind == KindUnknown || d.Kind == KindTest:
			continue
		case d.Kind.External():
			external = d
			externals++
			continue
		}
		if home != nil {
			return nil
		}
		home = d
	}
	if home == nil && externals == 1 {
		return external
	}
	return home
}

// ActionsInFile returns the actions defined in fileName (relative to the
// project root, as in compile output), in display order: the file's main
// table, view or operation comes before the assertions generated from it.
func (g *Graph) ActionsInFile(fileName string) []*Action {
	var out []*Action
	for _, a := range g.Actions {
		if a.Kind != KindUnknown && a.FileName == fileName {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(x, y *Action) int {
		return cmp.Or(cmp.Compare(x.Kind, y.Kind), compareActions(x, y))
	})
	return out
}

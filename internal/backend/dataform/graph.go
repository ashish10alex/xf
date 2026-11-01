package dataform

import (
	"encoding/json"
	"fmt"

	"github.com/ashish10alex/xf/internal/project"
)

// ParseCompileJSON builds a Graph from a compile result (see ExtractCompileJSON).
func ParseCompileJSON(data []byte) (*project.Graph, error) {
	var raw compiledJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode compile result: %w", err)
	}
	g := buildGraph(raw)
	g.JSON = data
	return g, nil
}

func buildGraph(raw compiledJSON) *project.Graph {
	var actions []*project.Action
	add := func(items []compiledAction, kind func(compiledAction) project.ActionKind) {
		for _, c := range items {
			actions = append(actions, newAction(c, kind(c)))
		}
	}
	add(raw.Tables, tableKind)
	add(raw.Operations, fixedKind(project.KindOperation))
	add(raw.Assertions, fixedKind(project.KindAssertion))
	add(raw.Notebooks, fixedKind(project.KindNotebook))
	add(raw.PropertyGraphs, fixedKind(project.KindPropertyGraph))
	add(raw.Declarations, fixedKind(project.KindDeclaration))

	g := project.NewGraph(actions)
	g.CoreVersion = raw.DataformCoreVersion
	g.DefaultDatabase = raw.ProjectConfig.DefaultDatabase
	g.DefaultLocation = raw.ProjectConfig.DefaultLocation
	pc := raw.ProjectConfig
	g.DefaultSchema, g.TablePrefix, g.SchemaSuffix, g.DatabaseSuffix = pc.DefaultSchema, pc.TablePrefix, pc.SchemaSuffix, pc.DatabaseSuffix
	for _, e := range raw.GraphErrors.CompilationErrors {
		g.Errors = append(g.Errors, project.CompilationError{FileName: project.SlashPath(e.FileName), Message: e.Message, Stack: e.Stack})
	}
	return g
}

func newAction(c compiledAction, kind project.ActionKind) *project.Action {
	t := toTarget(c.Target)
	a := &project.Action{
		ID:                 t.ID(),
		Target:             t,
		CanonicalTarget:    t,
		Kind:               kind,
		FileName:           project.SlashPath(c.FileName),
		Tags:               c.Tags,
		Disabled:           c.Disabled,
		Query:              c.Query,
		IncrementalQuery:   c.IncrementalQuery,
		PreOps:             c.PreOps,
		PostOps:            c.PostOps,
		IncrementalPreOps:  c.IncrementalPreOps,
		IncrementalPostOps: c.IncrementalPostOps,
		Queries:            c.Queries,
		HasOutput:          c.HasOutput,
		NotebookContents:   c.NotebookContents,
		GraphBody:          c.GraphBody,
		Description:        c.ActionDescriptor.Description,
		PartitionBy:        c.Bigquery.PartitionBy,
	}
	for _, col := range c.ActionDescriptor.Columns {
		a.Columns = append(a.Columns, project.ColumnDescription{Path: col.Path, Description: col.Description})
	}
	if c.CanonicalTarget.Name != "" {
		a.CanonicalTarget = toTarget(c.CanonicalTarget)
	}
	for _, d := range c.DependencyTargets {
		a.DependencyTargets = append(a.DependencyTargets, toTarget(d))
	}
	if c.ParentAction != nil {
		p := toTarget(*c.ParentAction)
		a.ParentAction = &p
	}
	return a
}

func toTarget(c compiledTarget) project.Target {
	return project.Target(c)
}

func tableKind(c compiledAction) project.ActionKind {
	switch c.Type {
	case "view":
		return project.KindView
	case "incremental":
		return project.KindIncremental
	default:
		return project.KindTable
	}
}

func fixedKind(k project.ActionKind) func(compiledAction) project.ActionKind {
	return func(compiledAction) project.ActionKind { return k }
}

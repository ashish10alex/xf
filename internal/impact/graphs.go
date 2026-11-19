package impact

import (
	"strings"

	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

// GraphReaders is the Dataform property graphs of g that read column of
// table, or table itself when column is "". BigQuery lineage doesn't
// record what CREATE PROPERTY GRAPH reads, so they come from the compile.
//
// A graph reads a column that a property's expression names (as the
// property, an exact copy when the property is the column itself), that
// its node exposes with PROPERTIES ALL COLUMNS (as a property of the same
// name), or that it names as a key or an edge's source or destination
// column (with no column of its own). Either way the graph loses what its
// queries read, and a key or a named property fails its rebuild.
func GraphReaders(g *project.Graph, table, column string) []lineage.ColumnLink {
	if g == nil {
		return nil
	}
	var out []lineage.ColumnLink
	for _, a := range g.Actions {
		if a.Kind != project.KindPropertyGraph {
			continue
		}
		out = append(out, graphReads(a, table, column)...)
	}
	return out
}

func graphReads(a *project.Action, table, column string) []lineage.ColumnLink {
	link := func(col string, dep lineage.DependencyType) lineage.ColumnLink {
		return lineage.ColumnLink{Table: string(a.ID), Column: col, DependencyType: dep}
	}
	var out []lineage.ColumnLink
	named := false
	for _, t := range a.GraphTables {
		if string(t.Source.ID()) != table {
			continue
		}
		if column == "" {
			return []lineage.ColumnLink{link("", lineage.Other)}
		}
		for _, p := range t.Properties {
			switch {
			case strings.EqualFold(strings.Trim(p.Expression, "`"), column):
				out = append(out, link(p.Name, lineage.ExactCopy))
			case Mentions(p.Expression, column):
				out = append(out, link(p.Name, lineage.Other))
			}
		}
		if t.AllColumns {
			out = append(out, link(column, lineage.ExactCopy))
		}
		for _, c := range t.Columns {
			named = named || strings.EqualFold(strings.Trim(c, "`"), column)
		}
	}
	if named && len(out) == 0 {
		out = append(out, link("", lineage.Other))
	}
	return out
}

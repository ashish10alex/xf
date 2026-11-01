// Package lineage finds who reads a BigQuery table's columns, from Dataplex
// Data Lineage: the links Dataplex records from the jobs that built each
// table, kept for 30 days and ingested about two hours after a job runs. It
// ports vscode-dataform-tools' column lineage sources
// (src/shared/columnLineage/dataplexLinks.ts and
// src/columnLineage/dataplexSource.ts), which stay the reference: keep the
// two in step, test cases included. Only the downstream direction is ported,
// since column impact asks who reads a column, not where it comes from.
package lineage

import (
	"context"
	"regexp"
	"strings"
)

// DependencyType is how a column was derived from the one it links to.
type DependencyType string

const (
	// ExactCopy is a column copied unchanged.
	ExactCopy DependencyType = "EXACT_COPY"
	// Other is a column used in an expression, a filter or a join.
	Other DependencyType = "OTHER"
	// TableOnly is a table-level link with no column detail.
	TableOnly DependencyType = "TABLE_ONLY"
)

// ColumnLink is one hop of lineage, seen from the column it was asked about.
type ColumnLink struct {
	// Table is the other end's project.dataset.table.
	Table string `json:"table"`
	// Column is "" when only a table-level link exists, e.g. the reader is
	// an incremental table.
	Column         string         `json:"column,omitempty"`
	DependencyType DependencyType `json:"dependencyType"`
	// Assertion is set when the other end is an assertion, e.g. a built-in
	// uniqueKey or nonNull check.
	Assertion bool `json:"assertion,omitempty"`
}

// EntityReference is one end of a Data Lineage API link.
type EntityReference struct {
	FullyQualifiedName string   `json:"fullyQualifiedName,omitempty"`
	Field              []string `json:"field,omitempty"`
}

// Link is the part of a Data Lineage API link that lineage uses.
type Link struct {
	Source         *EntityReference `json:"source,omitempty"`
	Target         *EntityReference `json:"target,omitempty"`
	DependencyInfo []struct {
		DependencyType string `json:"dependencyType"`
	} `json:"dependencyInfo,omitempty"`
}

const bigQueryPrefix = "bigquery:"

// Field is a column as Dataplex matches it: Dataplex stores column names
// lowercased and matches field case-sensitively, so a request for PLAYER_ID
// finds nothing while player_id finds its links.
func Field(column string) []string { return []string{strings.ToLower(column)} }

// FQN is a table as Dataplex names it.
func FQN(table string) string { return bigQueryPrefix + table }

var dataformTemp = regexp.MustCompile(`(?i)^df_[0-9a-f]+_table_(temp|empty)$`)

// IsTempTable reports tables a script creates and drops while it runs:
// BigQuery's hidden _script… datasets, and Dataform's df_<hash>_table_temp
// and df_<hash>_table_empty. Dataplex records links to them, but they're
// gone by the time anyone looks.
func IsTempTable(table string) bool {
	parts := strings.Split(table, ".")
	var dataset, name string
	if len(parts) > 1 {
		dataset = parts[1]
	}
	if len(parts) > 2 {
		name = parts[2]
	}
	return strings.HasPrefix(dataset, "_script") || dataformTemp.MatchString(name)
}

// bigQueryTable is the project.dataset.table of a BigQuery entity, or ""
// for anything else, temp tables included.
func bigQueryTable(ref *EntityReference) string {
	if ref == nil || !strings.HasPrefix(ref.FullyQualifiedName, bigQueryPrefix) {
		return ""
	}
	table := strings.TrimPrefix(ref.FullyQualifiedName, bigQueryPrefix)
	if IsTempTable(table) {
		return ""
	}
	return table
}

// dependencyOf is ExactCopy when any part of the link is a straight copy;
// anything else (OTHER, unspecified) is a transformation.
func dependencyOf(l Link) DependencyType {
	for _, info := range l.DependencyInfo {
		if info.DependencyType == string(ExactCopy) {
			return ExactCopy
		}
	}
	return Other
}

// ColumnLinksFromAPI is the column at the downstream end of each link, one
// entry per column. A column reached by several links (once as a copy and
// once through a filter, say) keeps the strongest dependency.
func ColumnLinksFromAPI(links []Link) []ColumnLink {
	var out []ColumnLink
	index := map[string]int{}
	for _, l := range links {
		end := l.Target
		table := bigQueryTable(end)
		if table == "" || len(end.Field) == 0 || end.Field[0] == "" {
			continue
		}
		column := end.Field[0]
		key := table + "#" + column
		dep := dependencyOf(l)
		i, ok := index[key]
		switch {
		case !ok:
			index[key] = len(out)
			out = append(out, ColumnLink{Table: table, Column: column, DependencyType: dep})
		case dep == ExactCopy && out[i].DependencyType != ExactCopy:
			out[i].DependencyType = ExactCopy
		}
	}
	return out
}

// TablesFromAPI is the tables at the downstream end of table-level links,
// each once, in the order first seen.
func TablesFromAPI(links []Link) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range links {
		if table := bigQueryTable(l.Target); table != "" && !seen[table] {
			seen[table] = true
			out = append(out, table)
		}
	}
	return out
}

// UntrackedReaders is the readers that read the table but whose columns
// Dataplex doesn't track, e.g. a job past its 1,500 column-link limit, which
// keeps only table-level lineage. A reader Dataplex does track, but that has
// no link for this column, doesn't read the column and is left out. So is a
// dev run of an action whose prod run has a link, and the other way round:
// toProd (nil for none) folds each into its action's prod table.
// hasColumnLineage is asked about every reader without a link, at once.
func UntrackedReaders(ctx context.Context, readers []string, withLink map[string]bool, hasColumnLineage func(ctx context.Context, table string) bool, toProd func(string) string) []string {
	if toProd == nil {
		toProd = func(t string) string { return t }
	}
	linked := map[string]bool{}
	for t := range withLink {
		linked[toProd(t)] = true
	}
	var candidates []string
	for _, r := range readers {
		if !linked[toProd(r)] {
			candidates = append(candidates, r)
		}
	}
	tracked := make([]bool, len(candidates))
	done := make(chan struct{}, len(candidates))
	for i, c := range candidates {
		go func() {
			tracked[i] = hasColumnLineage(ctx, c)
			done <- struct{}{}
		}()
	}
	for range candidates {
		<-done
	}
	var out []string
	for i, c := range candidates {
		if !tracked[i] {
			out = append(out, c)
		}
	}
	return out
}

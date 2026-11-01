package web

import (
	"cmp"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// externalRow is a row of the declarations table (ADR 0015): a
// declaration or dbt source of the selected action's file.
type externalRow struct {
	actionRef
	// At is the line naming it in its file, nil when no line does (a name
	// a loop builds).
	At *placeView `json:"at,omitempty"`
	// Updated is when its table was last updated, once it has been looked
	// up: only the selected declaration's table is (ADR 0015).
	Updated *detail `json:"updated,omitempty"`
}

// externalsLocked are the declarations table's rows for a, nil when a's
// file isn't one of external actions alone (see frontend.ExternalsInFile).
// Their places are set by placeExternals, once the lock is let go.
func (p *projectState) externalsLocked(g *project.Graph, a *project.Action) ([]externalRow, []*project.Action) {
	list := frontend.ExternalsInFile(g, a)
	if list == nil {
		return nil, nil
	}
	rows := make([]externalRow, len(list))
	for i, b := range list {
		rows[i].actionRef = refOf(b)
		if !hasTable(b) || p.tables[tableRefOf(b)] == nil {
			continue
		}
		if d, ok := p.updatedLocked(b); ok {
			rows[i].Updated = &d
		}
	}
	return rows, list
}

// placeExternals sets where each row is named in the file, reading it
// once, and sorts the rows into file order: those no line names last.
func (p *projectState) placeExternals(rows []externalRow, list []*project.Action) []externalRow {
	srcs := map[string][]byte{}
	np := navigate.Project{Root: p.deps.Root, ReadFile: func(path string) ([]byte, error) {
		if b, ok := srcs[path]; ok {
			return b, nil
		}
		b, err := os.ReadFile(path)
		if err == nil {
			srcs[path] = b
		}
		return b, err
	}}
	for i, a := range list {
		loc, ok := navigate.NamedAt(np, a)
		if !ok {
			continue
		}
		at := placeView{Line: loc.Line}
		if lines := strings.Split(string(srcs[loc.File]), "\n"); loc.Line < len(lines) {
			at.Col = unitsAt(lines[loc.Line], min(loc.Col, len(lines[loc.Line])))
		}
		rows[i].At = &at
	}
	slices.SortStableFunc(rows, func(x, y externalRow) int {
		if x.At == nil || y.At == nil {
			return cmp.Compare(boolInt(x.At == nil), boolInt(y.At == nil))
		}
		return cmp.Or(cmp.Compare(x.At.Line, y.At.Line), cmp.Compare(x.At.Col, y.At.Col))
	})
	return rows
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func tableRefOf(a *project.Action) bigquery.TableRef {
	return bigquery.TableRef{Project: a.Target.Database, Dataset: a.Target.Schema, Table: a.Target.Name}
}

// externalTableLocked is the lookup of a's table, nil until one was asked
// for.
func (p *projectState) externalTableLocked(a *project.Action) *tableEntry {
	if !hasTable(a) {
		return nil
	}
	return p.tables[tableRefOf(a)]
}

// externalColumnsLocked are a declaration's or source's columns, from its
// table in BigQuery: it has no SQL to dry run.
func (p *projectState) externalColumnsLocked(a *project.Action) columnsView {
	why := func(reason string, failed bool) columnsView {
		return columnsView{Fields: []bigquery.Field{}, Reason: reason, Failed: failed}
	}
	if p.deps.Table == nil || !hasTable(a) {
		return why("no BigQuery here, so no columns", false)
	}
	e := p.externalTableLocked(a)
	var apiErr *bigquery.APIError
	switch {
	case e == nil || !e.done:
		return why("looking up the table…", false)
	case errors.As(e.err, &apiErr) && apiErr.Status == http.StatusNotFound:
		return why("no such table in BigQuery", false)
	case errors.Is(e.err, bigquery.ErrAuth):
		return why("no credentials to look up the table", true)
	case apiErr != nil:
		return why("✗ "+firstLine(apiErr.Message), true)
	case e.err != nil:
		return why("✗ "+firstLine(e.err.Error()), true)
	}
	fields := frontend.FlattenFields(e.tbl.Schema)
	if len(fields) == 0 {
		return why("the table has no columns", false)
	}
	desc := map[string]string{}
	for _, c := range a.Columns {
		desc[strings.Join(c.Path, ".")] = c.Description
	}
	for i := range fields {
		if fields[i].Description == "" {
			fields[i].Description = desc[fields[i].Name]
		}
	}
	return columnsView{Fields: fields}
}

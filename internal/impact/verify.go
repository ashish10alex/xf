package impact

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/ashish10alex/xf/internal/project"
)

// What a dry run of a reader found, reading the changed table as the
// branch builds it (see Reader.Checked).
const (
	// CheckedClear: the dry run passed, so the reader doesn't read the
	// column any more, whatever its SQL still names.
	CheckedClear = "clear"
	// CheckedReads: BigQuery couldn't find the column, so the reader still
	// reads it.
	CheckedReads = "reads"
)

// verifyReaders settles the readers that a text search can't: those this
// branch changes whose new SQL still names a dropped column. Each is dry
// run with every reference to the changed table swapped for that table's
// own query on the branch, so BigQuery resolves the names: a pass means it
// no longer reads the column, "Unrecognized name" that it does. Any other
// failure leaves the reader as it was. progress is told after each run.
func verifyReaders(ctx context.Context, tables []Table, g *project.Graph, deps Deps, progress func(done, total int)) {
	type check struct {
		reader *Reader
		column string
		table  *project.Action
	}
	var checks []check
	for ti := range tables {
		t := &tables[ti]
		changed := g.Actions[project.ActionID(t.Table)]
		if t.Deleted || changed == nil {
			continue
		}
		for ci := range t.Columns {
			col := &t.Columns[ci]
			if col.Change.Kind != Dropped || col.Change.OnBase {
				continue
			}
			for ri := range col.Readers {
				if r := &col.Readers[ri]; r.StillNamed {
					checks = append(checks, check{r, col.Column, changed})
				}
			}
		}
	}
	if len(checks) == 0 {
		return
	}
	var mu sync.Mutex
	done := 0
	progress(0, len(checks))
	inBatches(ctx, checks, dryRunBatch, func(c check) {
		result := checkReader(ctx, c.reader.Table, c.column, c.table, g, deps)
		mu.Lock()
		if result != "" {
			c.reader.Checked = result
			if result == CheckedClear {
				c.reader.StillNamed, c.reader.ProbablyUpdated = false, true
			}
		}
		done++
		progress(done, len(checks))
		mu.Unlock()
	})
	for ti := range tables {
		for ci := range tables[ti].Columns {
			tables[ti].Columns[ci].Readers = RankReaders(tables[ti].Columns[ci].Readers)
		}
	}
}

// checkReader dry runs reader with changed's references swapped for its
// query: CheckedClear when it passes, CheckedReads when BigQuery can't
// find column, "" when there is no telling.
func checkReader(ctx context.Context, reader, column string, changed *project.Action, g *project.Graph, deps Deps) string {
	a := g.Actions[project.ActionID(reader)]
	if a == nil || a.Kind == project.KindOperation || changed.Query == "" {
		return ""
	}
	queries := a.DryRunQueries() // the full rebuild: an incremental run reads the same columns and more of itself
	project, location := g.DryRunTarget(a)
	if len(queries) == 0 || project == "" {
		return ""
	}
	sql, ok := swapTable(queries[0].SQL, changed.Target, changed.Query)
	if !ok {
		return ""
	}
	_, err := deps.DryRun(ctx, project, location, sql)
	switch {
	case err == nil:
		return CheckedClear
	case missingColumn(errorText(err), column):
		return CheckedReads
	}
	return ""
}

// swapTable replaces every reference to target in sql, as Dataform
// compiles them (`project.dataset.table`, or each part quoted on its own),
// with query as a subquery; false when sql has none.
func swapTable(sql string, target project.Target, query string) (string, bool) {
	parts := []string{target.Database, target.Schema, target.Name}
	if target.Database == "" {
		parts = parts[1:]
	}
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = regexp.QuoteMeta(p)
	}
	whole := "`" + strings.Join(quoted, `\.`) + "`"
	each := "`" + strings.Join(quoted, "`\\.`") + "`"
	re := regexp.MustCompile(`(?i)` + whole + `|` + each)
	if !re.MatchString(sql) {
		return "", false
	}
	sub := "(\n" + strings.TrimRight(strings.TrimSpace(query), ";") + "\n)"
	return re.ReplaceAllLiteralString(sql, sub), true
}

// missingColumn says whether a BigQuery error is about column not being
// there: "Unrecognized name: X", or "Name X not found inside Y".
func missingColumn(message, column string) bool {
	c := regexp.QuoteMeta(column)
	re := regexp.MustCompile(`(?i)(unrecognized name: ` + c + `|name ` + c + ` not found inside)([^A-Za-z0-9_$]|$)`)
	return re.MatchString(message)
}

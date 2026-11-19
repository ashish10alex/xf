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
	if deps.OnDemand {
		return // only the changed models wake a warehouse that bills
	}
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
	reads := queries[0].SQL
	if !a.RawOps && callsProcedure(a.PreOps) {
		// A pass would prove nothing of the query that reads changed: it is
		// dry run alone
		reads = a.Query
	}
	sql, ok := swapTable(reads, changed.Target, changed.Query)
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

// codeOnly is sql with its comments and string literals blanked out, so
// that what is left is code at the same byte positions: --, # and block
// comments, and quoted strings, triple-quoted ones included. Backticked
// names are kept.
func codeOnly(sql string) string {
	out := []byte(sql)
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--") || sql[i] == '#':
			to := len(sql)
			if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
				to = i + j
			}
			blank(i, to)
			i = to
		case strings.HasPrefix(sql[i:], "/*"):
			to := len(sql)
			if j := strings.Index(sql[i+2:], "*/"); j >= 0 {
				to = i + 2 + j + 2
			}
			blank(i, to)
			i = to
		case sql[i] == '\'' || sql[i] == '"':
			quote := sql[i : i+1]
			if strings.HasPrefix(sql[i:], strings.Repeat(quote, 3)) {
				quote = strings.Repeat(quote, 3)
			}
			j := i + len(quote)
			for j < len(sql) && !strings.HasPrefix(sql[j:], quote) {
				if sql[j] == '\\' {
					j++
				}
				j++
			}
			to := min(len(sql), j+len(quote))
			blank(i, to)
			i = to
		case sql[i] == '`':
			if j := strings.IndexByte(sql[i+1:], '`'); j >= 0 {
				i += j + 2
			} else {
				i = len(sql)
			}
		default:
			i++
		}
	}
	return string(out)
}

// notAliases are words that may follow a table in FROM without being its
// alias.
var notAliases = map[string]bool{
	"where": true, "join": true, "inner": true, "left": true, "right": true, "full": true, "cross": true, "outer": true,
	"on": true, "using": true, "group": true, "order": true, "limit": true, "union": true, "except": true, "intersect": true,
	"window": true, "qualify": true, "having": true, "for": true, "tablesample": true, "pivot": true, "unpivot": true,
	"with": true, "when": true, "then": true, "select": true, "natural": true, "lateral": true,
}

var aliasAfter = regexp.MustCompile("(?i)^\\s+(as\\s+)?([A-Za-z_][A-Za-z0-9_]*|`[^`]+`)")

// swapTable replaces every reference to target in sql, as Dataform
// compiles them (`project.dataset.table`, or each part quoted on its own),
// with query as a subquery; false when there is no telling. A reference in
// a comment or a string isn't one, and a reference written another way
// (unquoted, or without its project) would still read the table, so none
// may be left. A subquery has no implicit alias, so one without an alias is
// given the table's name.
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
	code := codeOnly(sql)
	matches := re.FindAllStringIndex(code, -1)
	if len(matches) == 0 {
		return "", false
	}
	sub := "(\n" + strings.TrimRight(strings.TrimSpace(query), ";") + "\n)"
	var swapped, left strings.Builder
	from := 0
	for _, m := range matches {
		swapped.WriteString(sql[from:m[0]])
		swapped.WriteString(sub)
		if a := aliasAfter.FindStringSubmatch(code[m[1]:]); a == nil || a[1] == "" && notAliases[strings.ToLower(a[2])] {
			swapped.WriteString(" AS `" + target.Name + "`")
		}
		left.WriteString(code[from:m[0]])
		left.WriteString(strings.Repeat(" ", m[1]-m[0]))
		from = m[1]
	}
	swapped.WriteString(sql[from:])
	left.WriteString(code[from:])
	if Mentions(left.String(), target.Schema+"."+target.Name) {
		return "", false
	}
	return swapped.String(), true
}

var callStatement = regexp.MustCompile(`(?i)\bCALL\b`)

// callsProcedure says whether any of stmts CALLs a procedure: BigQuery
// doesn't check what follows a CALL in a script it dry runs.
func callsProcedure(stmts []string) bool {
	for _, s := range stmts {
		if callStatement.MatchString(codeOnly(s)) {
			return true
		}
	}
	return false
}

// missingColumn says whether a BigQuery error is about column not being
// there: "Unrecognized name: X", or "Name X not found inside Y".
func missingColumn(message, column string) bool {
	c := regexp.QuoteMeta(column)
	re := regexp.MustCompile(`(?i)(unrecognized name: ` + c + `|name ` + c + ` not found inside)([^A-Za-z0-9_$]|$)`)
	return re.MatchString(message)
}

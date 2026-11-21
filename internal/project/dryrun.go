package project

import (
	"regexp"
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

// StripOptions controls what StripSQL blanks out in addition to comments.
type StripOptions struct {
	// BlankSingleStrings blanks single-quoted string literals ('...', ”'...”', r'...', rb'...').
	BlankSingleStrings bool
	// BlankDoubleStrings blanks double-quoted literals ("...", """...""", r"...", rb"...").
	// Leave false when double quotes may denote identifiers (e.g. Snowflake/ANSI SQL).
	BlankDoubleStrings bool
}

// StripSQL blanks SQL comments (--, # and /* */) and optionally string
// literals, leaving backticked identifiers intact. Handles triple-quoted
// strings and raw / raw-bytes prefixes (r, rb, br).
func StripSQL(sql string, opts StripOptions) string {
	var b strings.Builder
	b.Grow(len(sql))
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '`':
			j := i + 1
			for j < len(sql) && sql[j] != '`' {
				j++
			}
			j = min(j+1, len(sql))
			b.WriteString(sql[i:j])
			i = j
		case c == '\'' || c == '"':
			raw := isRawPrefix(sql, i)
			quote := sql[i : i+1]
			if strings.HasPrefix(sql[i:], strings.Repeat(quote, 3)) {
				quote = strings.Repeat(quote, 3)
			}
			j := i + len(quote)
			for j < len(sql) && !strings.HasPrefix(sql[j:], quote) {
				if !raw && sql[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+len(quote), len(sql))
			blank := (c == '\'' && opts.BlankSingleStrings) || (c == '"' && opts.BlankDoubleStrings)
			if blank {
				b.WriteByte(' ')
			} else {
				b.WriteString(sql[i:j])
			}
			i = j
		case c == '#' || c == '-' && strings.HasPrefix(sql[i:], "--"):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			if end := strings.Index(sql[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = len(sql)
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// StripComments blanks SQL comments (--, # and /* */), leaving strings
// (including triple-quoted and raw strings) and quoted names intact.
func StripComments(sql string) string {
	return StripSQL(sql, StripOptions{})
}

func isRawPrefix(sql string, quoteIdx int) bool {
	if quoteIdx >= 1 && (sql[quoteIdx-1] == 'r' || sql[quoteIdx-1] == 'R') && (quoteIdx == 1 || !isIdentByte(sql[quoteIdx-2])) {
		return true
	}
	if quoteIdx >= 2 && (quoteIdx == 2 || !isIdentByte(sql[quoteIdx-3])) {
		p := strings.ToLower(sql[quoteIdx-2 : quoteIdx])
		return p == "rb" || p == "br"
	}
	return false
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// writeDeniedRE is BigQuery's message for a dry run of a statement that
// writes to a table the caller may read but not write to.
var writeDeniedRE = regexp.MustCompile(`Permission (bigquery\.tables\.(?:updateData|create|delete|update)) denied on table (\S+)`)

// WriteDenied returns the table (project.dataset.table) and missing
// permission when msg is BigQuery refusing to dry run a write statement.
func WriteDenied(msg string) (table, permission string, ok bool) {
	m := writeDeniedRE.FindStringSubmatch(msg)
	if m == nil {
		return "", "", false
	}
	return strings.Replace(m[2], ":", ".", 1), m[1], true
}

// dmlRE is a statement that writes to a table.
var dmlRE = regexp.MustCompile(`(?i)^\s*(delete|insert|update|merge|truncate)\b`)

// WithoutDML returns q without the pre_operations that write to a table;
// ok is false when q has none.
func (q *DryRunQuery) WithoutDML() (*DryRunQuery, bool) {
	var kept []string
	var dropped bool
	for _, s := range q.PreOps {
		if dmlRE.MatchString(StripComments(s)) {
			dropped = true
			continue
		}
		kept = append(kept, s)
	}
	if !dropped {
		return nil, false
	}
	sql, _ := Script(append(slices.Clone(kept), q.Query))
	return &DryRunQuery{Label: q.Label, SQL: sql, PreOps: kept, Query: q.Query}, true
}

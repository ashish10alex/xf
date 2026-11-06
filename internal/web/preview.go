package web

// The preview: cmd+enter in an action's file runs the action's query in
// BigQuery, and the log pane's preview tab shows its rows in Scratch's
// grid. The page follows the job as Scratch follows its own
// (/api/scratch/job, /api/scratch/rows).

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// previewLabel is put on the jobs previews run.
const previewLabel = "xf-preview"

// previewView is the answer to a preview: the job it started, or what
// to ask before starting one.
type previewView struct {
	Job *bigquery.JobRef `json:"job,omitempty"`
	// SQL is what the job runs: the query, after the pre_operations it
	// needs.
	SQL string `json:"sql,omitempty"`
	// Name is the action's, as "dataset.name".
	Name string `json:"name"`
	// Ask is why the preview waits for a yes: what the dry run says it
	// scans, or a pre_operation that may change data.
	Ask    []string `json:"ask,omitempty"`
	Danger bool     `json:"danger,omitempty"`
}

// previewStatements are how the statements a preview runs before the query
// may begin: declaring and setting variables, temporary functions and
// tables. Any other pre_operation is asked about first, as it may change
// data (an incremental table's DELETE of the rows it is to rebuild).
var previewStatements = []string{"DECLARE ", "SET ", "CREATE TEMP ", "CREATE TEMPORARY ", "CREATE OR REPLACE TEMP ", "CREATE OR REPLACE TEMPORARY "}

// previewQuery is what a preview of a runs: its pre_operations and its
// query, the incremental ones when incremental, as one script. post
// operations never run. why says why there is nothing to preview.
func previewQuery(a *project.Action, incremental bool) (sql string, pre []string, why string) {
	switch a.Kind {
	case project.KindOperation:
		return "", nil, "an operation isn't previewed: it may change data, and returns no rows"
	case project.KindDeclaration, project.KindSource:
		return "", nil, "a " + a.Kind.String() + " has no query: its table's rows are in a Scratch query (s)"
	case project.KindNotebook, project.KindPropertyGraph, project.KindSeed, project.KindExposure, project.KindUnknown:
		return "", nil, "a " + a.Kind.String() + " has no query to preview"
	}
	query := a.Query
	pre = a.PreOps
	if a.Kind == project.KindIncremental && a.HasIncrementalVariant() && incremental {
		query, pre = a.IncrementalQuery, a.IncrementalPreOps
	}
	if a.RawOps {
		pre = nil
	}
	if a.Kind == project.KindAssertion || a.Kind == project.KindTest || a.Kind == project.KindSnapshot {
		pre = nil
	}
	if strings.TrimSpace(query) == "" {
		return "", nil, frontend.NoSQLMessage(a)
	}
	sql, _ = project.Script(append(slices.Clone(pre), query))
	return sql, pre, ""
}

// unsafePreOps are the pre_operations' statements that don't begin as
// previewStatements do, each by its first words.
func unsafePreOps(pre []string) []string {
	var out []string
	for _, op := range pre {
		for _, stmt := range strings.Split(op, ";") {
			s := strings.ToUpper(strings.Join(strings.Fields(stripComments(stmt)), " "))
			if s == "" || slices.ContainsFunc(previewStatements, func(p string) bool { return strings.HasPrefix(s+" ", p) }) {
				continue
			}
			words := strings.Fields(s)
			out = append(out, strings.Join(words[:min(len(words), 3)], " "))
		}
	}
	return out
}

// stripComments drops a statement's -- and /* */ comments, so its first
// word is its keyword.
func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "--") || s[i] == '#':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return b.String()
			}
			i += j
			b.WriteByte('\n')
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return b.String()
			}
			i += j + 3
			b.WriteByte(' ')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// preview starts a preview of an action (?id, or the action its file
// defines now), with the SQL pane 2 shows: incremental or not, reading
// from prod with defer to prod on. Unless force, it first dry runs it,
// and asks rather than starting a query that scans a lot or a
// pre_operation that may change data.
func (p *projectState) preview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          string `json:"id"`
		File        string `json:"file"`
		Incremental bool   `json:"incremental"`
		Force       bool   `json:"force"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p.mu.Lock()
	g := p.st.Graph
	if g == nil {
		p.mu.Unlock()
		writeError(w, errors.New("no compile result yet"))
		return
	}
	a := g.Actions[project.ActionID(req.ID)]
	if a == nil || a.Kind == project.KindUnknown {
		a = frontend.RenamedAction(g, project.ActionID(req.ID), req.File)
	}
	if a == nil {
		p.mu.Unlock()
		writeError(w, errors.New("no such action: select one to preview"))
		return
	}
	note := p.sqlNoteLocked(a)
	da := p.deferredLocked(a)
	proj, location := g.DryRunTarget(a)
	uncompiled := g.Notice != ""
	p.mu.Unlock()
	v := previewView{Name: a.Target.Schema + "." + a.Target.Name}
	switch {
	case uncompiled:
		writeError(w, errors.New("this project's SQL is templates BigQuery can't run"))
		return
	case note != "":
		writeError(w, errors.New("the SQL of "+v.Name+" is "+note+": preview it once it compiles"))
		return
	}
	sql, pre, why := previewQuery(da, req.Incremental)
	if why != "" {
		writeError(w, errors.New(why))
		return
	}
	v.SQL = sql
	if proj == "" {
		if c, err := p.bq(r.Context()); err == nil {
			proj = c.QuotaProject()
		}
	}
	if proj == "" {
		writeError(w, errors.New("no project to run the preview in: set defaultProject, or a quota project with gcloud"))
		return
	}
	if !req.Force {
		if bad := unsafePreOps(pre); len(bad) > 0 {
			v.Ask = append(v.Ask, "Its pre_operations run first, and one is a "+strings.Join(bad, ", ")+" statement, which may change data.")
			v.Danger = true
		}
		if p.deps.DryRun != nil {
			ctx, cancel := context.WithTimeout(r.Context(), scratchDryTimeout)
			d, err := p.deps.DryRun(ctx, proj, location, sql)
			cancel()
			if err != nil {
				writeError(w, queryError(err))
				return
			}
			if _, confirm := p.thresholds(); d.Bytes >= confirm {
				v.Ask = append(v.Ask, "The dry run says this scans "+bigquery.FormatBytes(d.Bytes)+" (≈ "+cost.FormatUSD(d.CostUSD())+" on demand).")
				v.Danger = true
			}
		}
		if len(v.Ask) > 0 {
			writeJSON(w, v)
			return
		}
	}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scratchCallLimit)
	defer cancel()
	ref, err := c.StartQuery(ctx, bigquery.QueryOptions{Project: proj, Location: location, SQL: sql, Labels: map[string]string{previewLabel: "true"}})
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	v.Job = &ref
	writeJSON(w, v)
}

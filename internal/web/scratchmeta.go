package web

// What the Scratch editor asks of the server beyond running queries:
// the tables and columns to complete, a table's documentation for its hover
// card, BigQuery's functions, and the filter that searches every row of a
// result in BigQuery.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/bqfuncs"
	"github.com/ashish10alex/xf/internal/project"
)

const (
	// metaMaxAge is how long a listing or a table's metadata is kept
	// before BigQuery is asked again; a DDL statement run from a scratch,
	// or the refresh button, clears it sooner.
	metaMaxAge     = 5 * time.Minute
	metaTimeout    = 30 * time.Second
	maxFilters     = 20
	maxFilterBytes = 500
)

type metaEntry struct {
	at time.Time
	v  any
}

// metaCache keeps what BigQuery said of datasets and tables, by key.
type metaCache struct {
	mu sync.Mutex
	m  map[string]metaEntry
}

func (c *metaCache) get(key string, now time.Time) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || now.Sub(e.at) > metaMaxAge {
		return nil, false
	}
	return e.v, true
}

func (c *metaCache) put(key string, v any, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]metaEntry{}
	}
	c.m[key] = metaEntry{at: now, v: v}
}

func (c *metaCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}

// clearsMeta reports whether a finished statement of this type can change
// which tables and columns there are.
func clearsMeta(statementType string) bool {
	for _, p := range []string{"CREATE_", "ALTER_", "DROP_", "SCRIPT"} {
		if strings.HasPrefix(statementType, p) {
			return true
		}
	}
	return false
}

// ---- BigQuery's functions ----

// scratchFunctions lists the functions the editor completes and explains;
// they are no project's, and are the same while xf runs.
func (s *Server) scratchFunctions(w http.ResponseWriter, r *http.Request) {
	all, err := bqfuncs.All()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, all)
}

// ---- the project's own models ----

type modelRef struct {
	Project     string `json:"project"`
	Dataset     string `json:"dataset"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
}

// scratchModels lists the tables the project's actions build or declare,
// for the first rows of a table completion.
func (p *projectState) scratchModels(w http.ResponseWriter, r *http.Request) {
	out := []modelRef{}
	if g := p.graph(); g != nil {
		for _, a := range g.Actions {
			// Assertions build views, but nobody queries them by name.
			if !hasOutputTable(a) || a.Target.Name == "" || a.Kind == project.KindAssertion {
				continue
			}
			out = append(out, modelRef{Project: a.Target.Database, Dataset: a.Target.Schema, Name: a.Target.Name, Kind: a.Kind.String(), Description: a.Description})
		}
	}
	slices.SortFunc(out, func(a, b modelRef) int {
		return strings.Compare(a.Dataset+"."+a.Name, b.Dataset+"."+b.Name)
	})
	writeJSON(w, out)
}

// ---- datasets and tables ----

func (p *projectState) scratchDatasets(w http.ResponseWriter, r *http.Request) {
	proj := r.URL.Query().Get("project")
	if proj == "" {
		http.Error(w, "bad request: project", http.StatusBadRequest)
		return
	}
	key := "ds:" + proj
	if v, ok := p.meta.get(key, p.deps.Now()); ok {
		writeJSON(w, v)
		return
	}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), metaTimeout)
	defer cancel()
	ds, err := c.ListDatasets(ctx, proj)
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	if ds == nil {
		ds = []string{}
	}
	p.meta.put(key, ds, p.deps.Now())
	writeJSON(w, ds)
}

func (p *projectState) scratchTables(w http.ResponseWriter, r *http.Request) {
	proj, ds := r.URL.Query().Get("project"), r.URL.Query().Get("dataset")
	if proj == "" || ds == "" {
		http.Error(w, "bad request: project and dataset", http.StatusBadRequest)
		return
	}
	key := "tb:" + proj + "." + ds
	if v, ok := p.meta.get(key, p.deps.Now()); ok {
		writeJSON(w, v)
		return
	}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), metaTimeout)
	defer cancel()
	ts, err := c.ListTables(ctx, proj, ds)
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	if ts == nil {
		ts = []bigquery.TableListing{}
	}
	p.meta.put(key, ts, p.deps.Now())
	writeJSON(w, ts)
}

// ---- one table: columns for completion, documentation for hover ----

type modelColumn struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type modelCard struct {
	Name        string        `json:"name"`
	Kind        string        `json:"kind"`
	File        string        `json:"file,omitempty"`
	Description string        `json:"description,omitempty"`
	Columns     []modelColumn `json:"columns,omitempty"`
}

type tableCard struct {
	Project string `json:"project"`
	Dataset string `json:"dataset"`
	Table   string `json:"table"`
	// Detail is BigQuery's word on the table; nil when it has none, and
	// Error says why: it doesn't exist (yet), or the call failed.
	Detail *bigquery.TableDetail `json:"detail,omitempty"`
	Error  string                `json:"error,omitempty"`
	// Model is the project's action that builds the table, if one does.
	Model *modelCard `json:"model,omitempty"`
}

func (p *projectState) modelOf(proj, ds, table string) *modelCard {
	g := p.graph()
	if g == nil {
		return nil
	}
	for _, a := range g.Actions {
		if a.Target.Database != proj || a.Target.Schema != ds || a.Target.Name != table || !hasOutputTable(a) {
			continue
		}
		m := &modelCard{Name: ds + "." + table, Kind: a.Kind.String(), File: a.FileName, Description: a.Description}
		for _, c := range a.Columns {
			m.Columns = append(m.Columns, modelColumn{Name: strings.Join(c.Path, "."), Description: c.Description})
		}
		return m
	}
	return nil
}

func (p *projectState) scratchTable(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	proj, ds, table := q.Get("project"), q.Get("dataset"), q.Get("table")
	if proj == "" || ds == "" || table == "" {
		http.Error(w, "bad request: project, dataset and table", http.StatusBadRequest)
		return
	}
	key := "t:" + proj + "." + ds + "." + table
	// A hover asks fresh: a table's last modified time must be as it is now.
	if q.Get("fresh") == "" {
		if v, ok := p.meta.get(key, p.deps.Now()); ok {
			writeJSON(w, v)
			return
		}
	}
	card := &tableCard{Project: proj, Dataset: ds, Table: table, Model: p.modelOf(proj, ds, table)}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), metaTimeout)
	defer cancel()
	d, err := c.Describe(ctx, proj, ds, table)
	switch apiErr, isAPI := errors.AsType[*bigquery.APIError](err); {
	case err == nil:
		card.Detail = d
	case errors.Is(err, bigquery.ErrAuth):
		writeError(w, err)
		return
	case isAPI && apiErr.Status == http.StatusNotFound:
		card.Error = "not found: it doesn't exist (yet)"
	case isAPI:
		card.Error = apiErr.Message
	default:
		writeError(w, err)
		return
	}
	p.meta.put(key, card, p.deps.Now())
	writeJSON(w, card)
}

// scratchRefresh drops what is kept of datasets and tables.
func (p *projectState) scratchRefresh(w http.ResponseWriter, r *http.Request) {
	p.meta.clear()
	writeJSON(w, okView{OK: true})
}

// ---- searching every row ----

type filterTerm struct {
	Column string `json:"column"`
	Value  string `json:"value"`
}

// sortTerm orders the rows by a column.
type sortTerm struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

type filterRequest struct {
	Project  string       `json:"project"`
	Location string       `json:"location"`
	ID       string       `json:"id"`
	Filters  []filterTerm `json:"filters"`
	Sort     []sortTerm   `json:"sort"`
	Confirm  bool         `json:"confirm"`
}

type filterView struct {
	// NeedsConfirm says the result's table is over the confirm size and
	// the request didn't say to go ahead.
	NeedsConfirm bool             `json:"needsConfirm,omitempty"`
	Bytes        int64            `json:"bytes"`
	Job          *bigquery.JobRef `json:"job,omitempty"`
}

// filterExpr is the SQL text of a column to look for a substring in.
func filterExpr(f bigquery.Field) string {
	col := "`" + strings.ReplaceAll(f.Name, "`", "\\`") + "`"
	switch strings.ToUpper(f.Type) {
	case "BYTES":
		if f.Mode != "REPEATED" {
			return "TO_BASE64(" + col + ")"
		}
	case "RECORD", "STRUCT", "JSON":
		return "TO_JSON_STRING(" + col + ")"
	}
	if f.Mode == "REPEATED" {
		return "TO_JSON_STRING(" + col + ")"
	}
	return "SAFE_CAST(" + col + " AS STRING)"
}

// orderable says BigQuery can ORDER BY a column of the type.
func orderable(f bigquery.Field) bool {
	if f.Mode == "REPEATED" {
		return false
	}
	switch strings.ToUpper(f.Type) {
	case "RECORD", "STRUCT", "JSON", "GEOGRAPHY", "INTERVAL", "RANGE":
		return false
	}
	return true
}

// filterSQL keeps the rows of table whose columns hold every term,
// ignoring case, in the order asked for; the terms are query parameters,
// never SQL.
func filterSQL(table bigquery.TableRef, schema []bigquery.Field, terms []filterTerm, sorts []sortTerm) (string, map[string]string, error) {
	params := map[string]string{}
	var where []string
	for _, t := range terms {
		if t.Value == "" {
			continue
		}
		i := slices.IndexFunc(schema, func(f bigquery.Field) bool { return f.Name == t.Column })
		if i < 0 {
			return "", nil, fmt.Errorf("the result has no column %q", t.Column)
		}
		name := fmt.Sprintf("f%d", len(params))
		params[name] = t.Value
		where = append(where, fmt.Sprintf("STRPOS(LOWER(%s), LOWER(@%s)) > 0", filterExpr(schema[i]), name))
	}
	var order []string
	for _, t := range sorts {
		i := slices.IndexFunc(schema, func(f bigquery.Field) bool { return f.Name == t.Column })
		if i < 0 {
			return "", nil, fmt.Errorf("the result has no column %q", t.Column)
		}
		if !orderable(schema[i]) {
			return "", nil, fmt.Errorf("BigQuery can't order by %s, a %s column", t.Column, strings.ToLower(schema[i].Type))
		}
		dir := ""
		if t.Desc {
			dir = " DESC"
		}
		order = append(order, "`"+strings.ReplaceAll(t.Column, "`", "\\`")+"`"+dir)
	}
	if len(where) == 0 && len(order) == 0 {
		return "", nil, errors.New("nothing to filter or sort by")
	}
	sql := fmt.Sprintf("SELECT * FROM `%s.%s.%s`", table.Project, table.Dataset, table.Table)
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	if len(order) > 0 {
		sql += " ORDER BY " + strings.Join(order, ", ")
	}
	return sql, params, nil
}

// scratchFilter starts a query that searches every row of a finished
// job's result, over the table BigQuery keeps it in.
func (p *projectState) scratchFilter(w http.ResponseWriter, r *http.Request) {
	var req filterRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Project == "" || req.ID == "" || len(req.Filters) > maxFilters || len(req.Sort) > 1 || len(req.Filters)+len(req.Sort) == 0 {
		http.Error(w, "bad request: project, id, up to 20 filters and at most one sort, and something to do", http.StatusBadRequest)
		return
	}
	for _, f := range req.Filters {
		if len(f.Value) > maxFilterBytes {
			http.Error(w, "bad request: a filter is 500 bytes at most", http.StatusBadRequest)
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
	ref := bigquery.JobRef{Project: req.Project, Location: req.Location, ID: req.ID}
	st, err := c.QueryJob(ctx, ref)
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	if st.ResultTable == nil {
		writeError(w, errors.New("BigQuery kept no table for this result, so only the loaded rows can be searched"))
		return
	}
	t := *st.ResultTable
	d, err := c.Describe(ctx, t.Project, t.Dataset, t.Table)
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	sql, params, err := filterSQL(t, d.Schema, req.Filters, req.Sort)
	if err != nil {
		writeError(w, err)
		return
	}
	v := filterView{Bytes: max(d.NumBytes, 0)}
	if _, confirm := p.thresholds(); !req.Confirm && v.Bytes >= confirm {
		v.NeedsConfirm = true
		writeJSON(w, v)
		return
	}
	job, err := c.StartQuery(ctx, bigquery.QueryOptions{Project: req.Project, Location: cmpOr(d.Location, req.Location), SQL: sql, Params: params,
		Labels: map[string]string{scratchLabel: "true"}})
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	v.Job = &job
	writeJSON(w, v)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

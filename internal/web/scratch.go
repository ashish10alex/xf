package web

// Scratch queries: SQL files in .xf/tmp that xf web edits and runs in
// BigQuery, a page of rows at a time. A scratch is made from the model the
// page is on; its file starts with a comment saying where it was made from
// and which project and location it runs in.

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/project"
)

const (
	// scratchDir is where scratches live, relative to the project root.
	scratchDir = ".xf/tmp"
	// scratchLabel is put on the jobs scratches run, so what they cost
	// can be told apart in billing exports.
	scratchLabel = "xf-scratch"
	// DefaultScratchWarnGB and DefaultScratchConfirmGB are the sizes of a
	// dry run at which the page turns it amber, and red and asks.
	DefaultScratchWarnGB    = config.DefaultScratchWarnGB
	DefaultScratchConfirmGB = config.DefaultScratchConfirmGB

	scratchDryTimeout = time.Minute
	scratchCallLimit  = time.Minute
	scratchCSVPage    = 5000
	maxScratchBytes   = 1 << 20
)

var scratchIDRe = regexp.MustCompile(`^scratch_[a-z0-9]{6,16}$`)

// scratchHeader is the comment a scratch's file begins with:
//
//	-- xf scratch: from dataset.model · project=acme location=EU
var scratchHeaderRe = regexp.MustCompile(`^-- xf scratch(?:: from (.*?))?(?: · project=(\S*))?(?: location=(\S*))?\s*$`)

type scratchMeta struct {
	From, Project, Location string
}

func (m scratchMeta) header() string {
	from := ""
	if m.From != "" {
		from = ": from " + m.From
	}
	return "-- xf scratch" + from + " · project=" + m.Project + " location=" + m.Location + "\n"
}

// parseScratch splits a scratch file into its header and the rest.
func parseScratch(text string) (scratchMeta, string) {
	first, rest, _ := strings.Cut(text, "\n")
	m := scratchHeaderRe.FindStringSubmatch(strings.TrimRight(first, "\r"))
	if m == nil {
		return scratchMeta{}, text
	}
	return scratchMeta{From: m[1], Project: m[2], Location: m[3]}, rest
}

// scratchFolder is where the project's scratches are kept.
func (p *projectState) scratchFolder() string {
	if p.scratchDirAbs != "" {
		return p.scratchDirAbs
	}
	return filepath.Join(p.deps.Root, filepath.FromSlash(scratchDir))
}

func (p *projectState) scratchPath(id string) (string, bool) {
	if !scratchIDRe.MatchString(id) {
		return "", false
	}
	return filepath.Join(p.scratchFolder(), id+".sql"), true
}

// scratchBoot is what the page starts from about scratch queries.
type scratchBoot struct {
	// Standalone says there is no project behind the page; Start that the
	// page opens on a new empty scratch.
	Standalone bool `json:"standalone"`
	Start      bool `json:"start"`
}

func newScratchID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "scratch_" + strconv.FormatInt(time.Now().Unix(), 36) + hex.EncodeToString(b)[:4]
}

// scratchView is one scratch as the page lists it.
type scratchView struct {
	ID       string    `json:"id"`
	From     string    `json:"from,omitempty"`
	Modified time.Time `json:"modified"`
	Preview  string    `json:"preview"`
}

// scratchFile is a scratch with its text.
type scratchFile struct {
	scratchView
	Project  string `json:"project"`
	Location string `json:"location"`
	SQL      string `json:"sql"`
}

func firstCodeLine(sql string) string {
	for _, l := range strings.Split(sql, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "--") {
			return shortenText(l, 100)
		}
	}
	return ""
}

func shortenText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (p *projectState) readScratch(id string) (*scratchFile, error) {
	path, ok := p.scratchPath(id)
	if !ok {
		return nil, errors.New("not a scratch: " + id)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	meta, sql := parseScratch(string(data))
	return &scratchFile{
		scratchView: scratchView{ID: id, From: meta.From, Modified: info.ModTime(), Preview: firstCodeLine(sql)},
		Project:     meta.Project, Location: meta.Location, SQL: sql,
	}, nil
}

func (p *projectState) scratchList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(p.scratchFolder())
	out := []scratchView{}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, err)
		return
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".sql")
		if !ok || e.IsDir() || !scratchIDRe.MatchString(id) {
			continue
		}
		if f, err := p.readScratch(id); err == nil {
			out = append(out, f.scratchView)
		}
	}
	slices.SortFunc(out, func(a, b scratchView) int { return cmp.Or(b.Modified.Compare(a.Modified), strings.Compare(b.ID, a.ID)) })
	writeJSON(w, out)
}

func (p *projectState) scratchGet(w http.ResponseWriter, r *http.Request) {
	f, err := p.readScratch(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, f)
}

// writeScratch writes a scratch's file, making .xf/tmp, which ignores
// itself in git, if it isn't there.
func (p *projectState) writeScratch(id string, m scratchMeta, sql string) error {
	path, ok := p.scratchPath(id)
	if !ok {
		return errors.New("not a scratch: " + id)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if p.scratchDirAbs == "" { // a project's folder may be committed
		ignore := filepath.Join(dir, ".gitignore")
		if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(ignore, []byte("# scratch queries xf web made: not for git\n*\n"), 0o644)
		}
	}
	return os.WriteFile(path, []byte(m.header()+sql), 0o644)
}

// scratchSave keeps the text the editor holds.
func (p *projectState) scratchSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID  string `json:"id"`
		SQL string `json:"sql"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.SQL) > maxScratchBytes {
		writeError(w, errors.New("a scratch holds 1 MiB of SQL at most"))
		return
	}
	f, err := p.readScratch(req.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := p.writeScratch(req.ID, scratchMeta{From: f.From, Project: f.Project, Location: f.Location}, req.SQL); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, okView{OK: true})
}

func (p *projectState) scratchDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	path, ok := p.scratchPath(req.ID)
	if !ok {
		writeError(w, errors.New("not a scratch: "+req.ID))
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, err)
		return
	}
	p.dropScratchRuns(req.ID)
	writeJSON(w, okView{OK: true})
}

// scratchCreate makes a scratch from an action: its compiled SQL, or a
// SELECT of the columns of the table it builds.
// scratchDefaultsView is the project and location a scratch with no model
// runs in.
type scratchDefaultsView struct {
	Project  string `json:"project"`
	Location string `json:"location"`
	// Standalone is set outside any project.
	Standalone bool `json:"standalone"`
}

// scratchDefaults finds them: what xf scratch was told, else the project's
// default database and location, else the credentials' quota project.
func (p *projectState) scratchDefaults(ctx context.Context) scratchDefaultsView {
	v := scratchDefaultsView{Project: p.scratchDefProject, Location: p.scratchDefLocation, Standalone: p.standalone}
	if g := p.graph(); g != nil {
		v.Project, v.Location = cmp.Or(v.Project, g.DefaultDatabase), cmp.Or(v.Location, g.DefaultLocation)
	}
	if v.Project == "" {
		v.Project, _ = p.scratchProject(ctx, "")
	}
	return v
}

func (p *projectState) scratchDefaultsHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, p.scratchDefaults(r.Context()))
}

// scratchTarget changes the project and location a scratch runs in, which
// its first line holds.
func (p *projectState) scratchTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id"`
		Project  string `json:"project"`
		Location string `json:"location"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	f, err := p.readScratch(req.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if strings.ContainsAny(req.Project+req.Location, " \t\r\n") {
		writeError(w, errors.New("a project or location has no spaces"))
		return
	}
	if err := p.writeScratch(req.ID, scratchMeta{From: f.From, Project: req.Project, Location: req.Location}, f.SQL); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, okView{OK: true})
}

func (p *projectState) scratchCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID          project.ActionID `json:"id"`
		Form        string           `json:"form"` // "compiled", "columns", "preview" or "empty"
		Incremental bool             `json:"incremental"`
		// Project and Location are for an empty scratch, which has no
		// model to take them from.
		Project  string `json:"project"`
		Location string `json:"location"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Form == "empty" {
		def := p.scratchDefaults(r.Context())
		m := scratchMeta{Project: cmp.Or(req.Project, def.Project), Location: cmp.Or(req.Location, def.Location)}
		id := newScratchID()
		if err := p.writeScratch(id, m, ""); err != nil {
			writeError(w, err)
			return
		}
		f, err := p.readScratch(id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, f)
		return
	}
	g := p.graph()
	var a *project.Action
	if g != nil {
		a = g.Actions[req.ID]
	}
	if a == nil {
		writeError(w, errors.New("no such action"))
		return
	}
	proj, location := g.DryRunTarget(a)
	if proj == "" {
		proj, _ = p.scratchProject(r.Context(), "")
	}
	var sql string
	switch req.Form {
	case "compiled":
		p.mu.Lock()
		note := p.sqlNoteLocked(a)
		p.mu.Unlock()
		if note != "" {
			writeError(w, errors.New("this action's SQL is "+note))
			return
		}
		queries := a.DryRunQueries()
		if len(queries) == 0 {
			writeError(w, fmt.Errorf("%s has no SQL to run", a.Kind))
			return
		}
		q := queries[0]
		for _, x := range queries {
			if x.Label != "" && (x.Label == "incremental") == req.Incremental {
				q = x
			}
		}
		sql = q.SQL + "\n"
	case "columns":
		if !hasOutputTable(a) {
			msg := fmt.Sprintf("%s builds no table to select from", a.Kind)
			if a.Kind == project.KindOperation {
				msg += " (it has no hasOutput: true)"
			}
			writeError(w, errors.New(msg))
			return
		}
		if p.deps.Table == nil {
			writeError(w, errors.New("no BigQuery access here, so no columns"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), tableTimeout)
		defer cancel()
		tbl, err := p.deps.Table(ctx, a.Target.Database, a.Target.Schema, a.Target.Name)
		var apiErr *bigquery.APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
			writeError(w, fmt.Errorf("%s.%s.%s does not exist yet: build it, or start from the compiled SQL", a.Target.Database, a.Target.Schema, a.Target.Name))
			return
		case err != nil:
			writeError(w, err)
			return
		case len(tbl.Schema) == 0:
			writeError(w, errors.New("the table has no columns"))
			return
		}
		if location == "" {
			location = tbl.Location
		}
		sql = selectColumns(a.Target.Database, a.Target.Schema, a.Target.Name, tbl.Schema)
	case "preview":
		// The declarations table's preview: left for the user to run, with
		// no lookup first, which a table they can't see would fail.
		if !hasOutputTable(a) {
			writeError(w, fmt.Errorf("%s has no table to preview", a.Kind))
			return
		}
		sql = "SELECT *\nFROM `" + a.Target.Database + "." + a.Target.Schema + "." + a.Target.Name + "`\nLIMIT 100\n"
	default:
		writeError(w, errors.New(`form must be "compiled", "columns" or "preview"`))
		return
	}
	id := newScratchID()
	from := string(a.ID)
	if a.Target.Name != "" {
		from = a.Target.Schema + "." + a.Target.Name
	}
	m := scratchMeta{From: strings.Join(strings.Fields(from), " "), Project: proj, Location: location}
	if err := p.writeScratch(id, m, sql); err != nil {
		writeError(w, err)
		return
	}
	f, err := p.readScratch(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, f)
}

// hasOutputTable reports whether a builds a table at its target to select
// from: a model does, and so does an operation with hasOutput.
func hasOutputTable(a *project.Action) bool {
	return hasTable(a) || a.Kind == project.KindOperation && a.HasOutput && a.Target.Database != ""
}

var plainIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reserved are the GoogleSQL words a column of that name needs backticks
// for.
var reserved = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`ALL AND ANY ARRAY AS ASC ASSERT_ROWS_MODIFIED AT BETWEEN BY CASE CAST COLLATE CONTAINS CREATE CROSS CUBE CURRENT DEFAULT DEFINE DESC DISTINCT ELSE END ENUM ESCAPE EXCEPT EXCLUDE EXISTS EXTRACT FALSE FETCH FOLLOWING FOR FROM FULL GROUP GROUPING GROUPS HASH HAVING IF IGNORE IN INNER INTERSECT INTERVAL INTO IS JOIN LATERAL LEFT LIKE LIMIT LOOKUP MERGE NATURAL NEW NO NOT NULL NULLS OF ON OR ORDER OUTER OVER PARTITION PRECEDING PROTO QUALIFY RANGE RECURSIVE RESPECT RIGHT ROLLUP ROWS SELECT SET SOME STRUCT TABLESAMPLE THEN TO TREAT TRUE UNBOUNDED UNION UNNEST USING WHEN WHERE WINDOW WITH WITHIN`) {
		reserved[w] = true
	}
}

func quoteIdent(s string) string {
	if plainIdent.MatchString(s) && !reserved[strings.ToUpper(s)] {
		return s
	}
	return "`" + strings.ReplaceAll(s, "`", "\\`") + "`"
}

// selectColumns is a SELECT of the table's columns, comma-separated and
// one to a line.
func selectColumns(proj, dataset, table string, schema []bigquery.Field) string {
	var b strings.Builder
	b.WriteString("SELECT\n")
	for i, f := range schema {
		b.WriteString("  " + quoteIdent(f.Name))
		if i < len(schema)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("FROM `" + proj + "." + dataset + "." + table + "`\n")
	return b.String()
}

// ---- the dry run ----

type scratchDryView struct {
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	Auth          bool   `json:"auth,omitempty"`
	Bytes         int64  `json:"bytes"`
	Accuracy      string `json:"accuracy,omitempty"`
	StatementType string `json:"statementType,omitempty"`
	// Text is the dry run as the Actions pane words it.
	Text string `json:"text,omitempty"`
	// Cost is what the bytes cost on demand, as "$0.032"; "" when the size
	// is unknown.
	Cost string `json:"cost,omitempty"`
	// Writes says the statement changes data or a schema, which the page
	// asks about before it runs.
	Writes bool             `json:"writes"`
	Tables []string         `json:"tables,omitempty"`
	Schema []bigquery.Field `json:"schema,omitempty"`
	// WarnBytes and ConfirmBytes are the sizes the page colours and asks at.
	WarnBytes    int64 `json:"warnBytes"`
	ConfirmBytes int64 `json:"confirmBytes"`
}

// readOnlyStatements are the statement types that change nothing.
var readOnlyStatements = map[string]bool{"SELECT": true, "": true}

func (p *projectState) thresholds() (warn, confirm int64) {
	const gb = 1 << 30
	w, c := p.scratchWarnGB, p.scratchConfirmGB
	if w <= 0 {
		w = DefaultScratchWarnGB
	}
	if c <= 0 {
		c = DefaultScratchConfirmGB
	}
	return int64(w * gb), int64(c * gb)
}

func (p *projectState) scratchDryRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID  string `json:"id"`
		SQL string `json:"sql"`
		// Project and Location are for a scratch that has no file yet.
		Project  string `json:"project"`
		Location string `json:"location"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v := scratchDryView{}
	v.WarnBytes, v.ConfirmBytes = p.thresholds()
	f := &scratchFile{Project: req.Project, Location: req.Location}
	if req.ID != "" {
		var err error
		if f, err = p.readScratch(req.ID); err != nil {
			writeError(w, err)
			return
		}
	}
	if p.deps.DryRun == nil {
		v.Error = "no dry runs here"
		writeJSON(w, v)
		return
	}
	if strings.TrimSpace(req.SQL) == "" {
		v.Error = "nothing to run"
		writeJSON(w, v)
		return
	}
	proj, ok := p.scratchProject(r.Context(), f.Project)
	if !ok {
		v.Error, v.Auth = "no project to dry run in: set one in the first line of the scratch, or a quota project with gcloud", false
		writeJSON(w, v)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scratchDryTimeout)
	defer cancel()
	d, err := p.deps.DryRun(ctx, proj, f.Location, req.SQL)
	if err != nil {
		v.Auth = errors.Is(err, bigquery.ErrAuth)
		v.Error = err.Error()
		if apiErr, ok := errors.AsType[*bigquery.APIError](err); ok {
			v.Error = apiErr.Message
		}
		writeJSON(w, v)
		return
	}
	v.OK, v.Bytes, v.Accuracy, v.StatementType, v.Schema = true, d.Bytes, d.Accuracy, d.StatementType, d.Schema
	// A script is any number of statements; the dry run can't tell which,
	// so it is asked about like a write.
	v.Writes = !readOnlyStatements[d.StatementType]
	for _, t := range d.Tables {
		v.Tables = append(v.Tables, t.Project+"."+t.Dataset+"."+t.Table)
	}
	v.Text = bigquery.FormatBytes(d.Bytes)
	if d.Accuracy == bigquery.AccuracyUnknown {
		v.Text = "unknown"
	} else {
		v.Cost = cost.FormatUSD(d.CostUSD())
	}
	writeJSON(w, v)
}

// scratchProject is the project a scratch's jobs are billed to: the one in
// its header, else the credentials' quota project.
func (p *projectState) scratchProject(ctx context.Context, header string) (string, bool) {
	if header != "" {
		return header, true
	}
	if c, err := p.bq(ctx); err == nil && c.QuotaProject() != "" {
		return c.QuotaProject(), true
	}
	return "", false
}

// ---- running ----

type jobRequest struct {
	ID  string `json:"id"`
	SQL string `json:"sql"`
}

func (p *projectState) bq(ctx context.Context) (*bigquery.Client, error) {
	if p.bigquery == nil {
		return nil, errors.New("no BigQuery access here")
	}
	return p.bigquery(ctx)
}

func (p *projectState) scratchRun(w http.ResponseWriter, r *http.Request) {
	var req jobRequest
	if !readJSON(w, r, &req) {
		return
	}
	f, err := p.readScratch(req.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(req.SQL) == "" {
		writeError(w, errors.New("nothing to run"))
		return
	}
	proj, ok := p.scratchProject(r.Context(), f.Project)
	if !ok {
		writeError(w, errors.New("no project to run in: set project=… in the first line of the scratch"))
		return
	}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scratchCallLimit)
	defer cancel()
	ref, err := c.StartQuery(ctx, bigquery.QueryOptions{Project: proj, Location: f.Location, SQL: req.SQL, Labels: map[string]string{scratchLabel: "true"}})
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	writeJSON(w, ref)
}

// queryError words a BigQuery error as its message, which carries the
// line and column of a mistake in the SQL.
func queryError(err error) error {
	if apiErr, ok := errors.AsType[*bigquery.APIError](err); ok {
		return errors.New(apiErr.Message)
	}
	return err
}

// rowsError is queryError, saying so when a result is gone: BigQuery
// keeps a query's result for about a day, and a recent run opened again
// may be older.
func rowsError(err error) error {
	if apiErr, ok := errors.AsType[*bigquery.APIError](err); ok && apiErr.Status == http.StatusNotFound {
		return errors.New("BigQuery no longer has this result (it keeps one for about 24 hours): run the query again")
	}
	return queryError(err)
}

func jobRefOf(r *http.Request) (bigquery.JobRef, bool) {
	q := r.URL.Query()
	ref := bigquery.JobRef{Project: q.Get("project"), Location: q.Get("location"), ID: q.Get("id")}
	return ref, ref.Project != "" && ref.ID != ""
}

// jobView is where a job stands: its status, and for a script its
// statements.
type jobView struct {
	*bigquery.QueryStatus
	Children []bigquery.ChildJob `json:"children,omitempty"`
	// Cost is what the billed bytes cost on demand, once the job is done.
	Cost string `json:"cost,omitempty"`
}

func (p *projectState) scratchJob(w http.ResponseWriter, r *http.Request) {
	ref, ok := jobRefOf(r)
	if !ok {
		http.Error(w, "bad request: project and id", http.StatusBadRequest)
		return
	}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scratchCallLimit)
	defer cancel()
	st, err := c.QueryJob(ctx, ref)
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	v := jobView{QueryStatus: st}
	if st.State == "DONE" {
		v.Cost = cost.FormatUSD(bigquery.CostUSD(st.BytesBilled))
	}
	if st.State == "DONE" && clearsMeta(st.StatementType) {
		p.meta.clear() // a table or column may have come or gone
	}
	if st.State == "DONE" && st.Error == "" && st.StatementType == "SCRIPT" {
		if v.Children, err = c.ChildJobs(ctx, ref); err != nil {
			writeError(w, queryError(err))
			return
		}
	}
	writeJSON(w, v)
}

func (p *projectState) scratchRows(w http.ResponseWriter, r *http.Request) {
	ref, ok := jobRefOf(r)
	if !ok {
		http.Error(w, "bad request: project and id", http.StatusBadRequest)
		return
	}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scratchCallLimit)
	defer cancel()
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	page, err := c.Rows(ctx, ref, r.URL.Query().Get("token"), min(size, 5000))
	if err != nil {
		writeError(w, rowsError(err))
		return
	}
	writeJSON(w, page)
}

func (p *projectState) scratchCancel(w http.ResponseWriter, r *http.Request) {
	var ref bigquery.JobRef
	if !readJSON(w, r, &ref) {
		return
	}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scratchCallLimit)
	defer cancel()
	if err := c.CancelQuery(ctx, ref); err != nil {
		writeError(w, queryError(err))
		return
	}
	writeJSON(w, okView{OK: true})
}

// scratchCSV streams every row of a job's result as CSV, a page at a time
// from BigQuery, so a large result is never held whole. Nested values are
// JSON in their cell, BYTES base64.
func (p *projectState) scratchCSV(w http.ResponseWriter, r *http.Request) {
	ref, ok := jobRefOf(r)
	if !ok {
		http.Error(w, "bad request: project and id", http.StatusBadRequest)
		return
	}
	c, err := p.bq(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	// Read the first page before the headers go out, so a failure is an
	// error the page can show rather than half a file.
	ctx := r.Context()
	page, err := c.Rows(ctx, ref, "", scratchCSVPage)
	if err != nil {
		writeError(w, queryError(err))
		return
	}
	if len(page.Schema) == 0 {
		writeError(w, errors.New("this statement has no rows to export"))
		return
	}
	name := strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 {
			return -1
		}
		return r
	}, cmp.Or(r.URL.Query().Get("name"), "result"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"; filename*=UTF-8''`+url.PathEscape(name)+".csv")
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(w)
	head := make([]string, len(page.Schema))
	for i, f := range page.Schema {
		head[i] = f.Name
	}
	_ = cw.Write(head)
	flusher, _ := w.(http.Flusher)
	for {
		for _, row := range page.Rows {
			rec := make([]string, len(row))
			for i, cell := range row {
				rec[i] = bigquery.CellText(cell)
			}
			if err := cw.Write(rec); err != nil {
				return
			}
		}
		cw.Flush()
		if flusher != nil {
			flusher.Flush()
		}
		if page.NextToken == "" || ctx.Err() != nil {
			return
		}
		if page, err = c.Rows(ctx, ref, page.NextToken, scratchCSVPage); err != nil {
			// The file is already on its way: end it with a row saying why,
			// which no data row can be mistaken for.
			_ = cw.Write([]string{"xf: export stopped: " + err.Error()})
			cw.Flush()
			return
		}
	}
}

// setScratch gives the project what scratch queries run with.
func (p *projectState) setScratch(o *Project) {
	p.bigquery, p.scratchWarnGB, p.scratchConfirmGB = o.BigQuery, o.ScratchWarnGB, o.ScratchConfirmGB
	p.scratchDirAbs, p.scratchDefProject, p.scratchDefLocation = o.ScratchDir, o.ScratchProject, o.ScratchLocation
	p.standalone, p.startScratch, p.startCheck = o.Standalone, o.StartScratch, o.StartCheck
	p.sourceAutosaveMs = o.SourceAutosaveMs
	p.diffStyle = o.DiffStyle
	p.pythonServer = o.PythonLanguageServer
	p.sqlfluffBin, p.sqlfluffConfig = o.SqlfluffBin, o.SqlfluffConfig
	p.autoHidePane2 = o.AutoHidePane2
	p.pane2Tab = o.Pane2Tab
	p.wrapLines = o.WrapLines
	p.vimKeys = o.VimKeys
}

package web

// The dependency inspector (ADR 0033): a page that lists a model and the
// tables upstream of it, each with a filter, and dry runs or runs
// SELECT * FROM <table> WHERE <filter> on them. Its runs are xf web's, as
// every run is: queued here, at most maxInspectorRuns at once, so leaving
// the page or reloading it leaves them running.

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

const (
	// maxInspectorRuns is how many of the inspector's queries run at once;
	// the rest wait their turn.
	maxInspectorRuns = 4
	// inspectorRunLimit bounds one query, from starting it to its row count.
	inspectorRunLimit = 30 * time.Minute
	// inspectorLabel is put on the jobs the inspector runs.
	inspectorLabel = "xf-inspector"
	// defaultInspectorDepth is the depth a model starts with, the
	// extension's.
	defaultInspectorDepth = 3
)

// inspectorRuns are the inspector's runs, by model key and then row key.
type inspectorRuns struct {
	byModel map[string]map[string]*inspectorRun
	// slots holds one token per running query.
	slots chan struct{}
}

// inspectorRun is one row's query, queued, running or ended.
type inspectorRun struct {
	Phase string           `json:"phase"` // queued, running, done, failed, cancelled
	SQL   string           `json:"sql"`
	Job   *bigquery.JobRef `json:"job,omitempty"`
	Rows  int64            `json:"rows"`
	// Cost is what the billed bytes cost on demand, once done.
	Cost   string    `json:"cost,omitempty"`
	Billed int64     `json:"billed"`
	Error  string    `json:"error,omitempty"`
	Ended  time.Time `json:"ended,omitzero"`
	cancel context.CancelFunc
}

// inspectorNode is a node of the walk as the page shows it.
type inspectorNode struct {
	frontend.UpstreamNode
	// Dev is the table the project builds; Read the one a query reads,
	// prod's when defer to prod reads it from there.
	Dev  string `json:"dev"`
	Read string `json:"read"`
	// Label, Tone and Hover say where it is read from, as pane 2's list
	// does, while defer to prod is on.
	Label string `json:"label,omitempty"`
	Tone  string `json:"tone,omitempty"`
	Hover string `json:"hover,omitempty"`
	// URL opens the table read in the BigQuery console.
	URL string `json:"url,omitempty"`
}

type inspectorView struct {
	Model project.ActionID `json:"model"`
	Key   string           `json:"key"`
	Name  string           `json:"name"`
	Depth int              `json:"depth"`
	// Error says why there is nothing to show: no compile, no such model.
	Error string                  `json:"error,omitempty"`
	Nodes []inspectorNode         `json:"nodes"`
	Edges []frontend.UpstreamEdge `json:"edges"`
	// Defer is set while defer to prod is on; DeferLoading while what it
	// reads from prod is being worked out.
	Defer        bool   `json:"defer,omitempty"`
	DeferLoading bool   `json:"deferLoading,omitempty"`
	DeferError   string `json:"deferError,omitempty"`
	// State is the model's page as last left, nil the first time.
	State *session.InspectorState `json:"state,omitempty"`
	// Missing are the tables added by hand that no action names now.
	Missing []string `json:"missing,omitempty"`
	// Runs are the rows' queries this xf web ran, by row key.
	Runs map[string]*inspectorRun `json:"runs"`
}

// inspectorSQL is the query of a row: every column of table, filtered.
func inspectorSQL(table, filter string) string {
	sql := "SELECT * FROM `" + table + "`"
	if f := strings.TrimSpace(filter); f != "" {
		sql += " WHERE " + f
	}
	return sql
}

// inspector answers the page for ?model, ?depth levels deep (the depth
// last left, else 3).
func (p *projectState) inspector(w http.ResponseWriter, r *http.Request) {
	model := project.ActionID(r.URL.Query().Get("model"))
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	v, starts := p.inspectorViewOf(model, depth)
	for _, f := range starts {
		go f()
	}
	writeJSON(w, v)
}

func (p *projectState) inspectorViewOf(model project.ActionID, depth int) (inspectorView, []func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := inspectorView{Model: model, Nodes: []inspectorNode{}, Edges: []frontend.UpstreamEdge{}, Runs: map[string]*inspectorRun{}}
	g := p.st.Graph
	if g == nil {
		v.Error = "the project hasn't compiled yet"
		return v, nil
	}
	a := g.Actions[model]
	if a == nil {
		v.Error = "no such action: " + string(model)
		return v, nil
	}
	v.Key, v.Name = frontend.PresetKey(a), a.Target.Name
	if st, ok := p.sess.Inspector[v.Key]; ok {
		v.State = &st
		if depth <= 0 {
			depth = st.Depth
		}
	}
	if depth <= 0 {
		depth = defaultInspectorDepth
	}
	v.Depth = min(depth, frontend.MaxUpstreamDepth)
	nodes, edges := frontend.Upstream(g, model, v.Depth)
	v.Edges = append(v.Edges, edges...)
	// Tables added by hand follow, unless the walk has them; each is read as
	// an action that reads it would read it, so defer to prod applies.
	via := edges
	if v.State != nil {
		listed := map[project.ActionID]bool{}
		for _, n := range nodes {
			listed[n.ID] = true
		}
		for _, key := range v.State.Added {
			a := frontend.ActionByPresetKey(g, key)
			if a == nil {
				v.Missing = append(v.Missing, key)
				continue
			}
			if listed[a.ID] {
				continue
			}
			listed[a.ID] = true
			nodes = append(nodes, frontend.AddedNode(a))
			if r := frontend.ReaderOf(g, a.ID); r != nil {
				via = append(slices.Clip(via), frontend.UpstreamEdge{From: r.ID, To: a.ID})
			}
		}
	}
	reads, starts := p.inspectorReadsLocked(g, &v, nodes, via)
	for _, n := range nodes {
		dev := string(n.ID)
		in := inspectorNode{UpstreamNode: n, Dev: dev, Read: dev}
		if dep := g.Actions[n.ID]; dep != nil {
			in.URL = dep.Target.ConsoleURL()
		}
		if h, ok := reads[n.ID]; ok {
			in.Label, in.Tone, in.Hover = h.Label, h.Tone, h.Hover
			// Read from prod, changed on this branch or not.
			if h.Prod != "" && (h.Tone == "prod" || strings.HasPrefix(h.Label, "⚠ prod")) {
				in.Read = h.Prod
			}
			if h.URL != "" {
				in.URL = h.URL
			}
		}
		v.Nodes = append(v.Nodes, in)
	}
	maps.Copy(v.Runs, p.insp.byModel[v.Key])
	return v, starts
}

// inspectorReadsLocked is where each node but the model is read from
// while defer to prod is on, by its ID: what the Deferral of a node that
// reads it says. Each such node's Deferral is asked for on its own, as
// it is for the action pane 2 shows, since a node is not a candidate of a
// request that names it.
func (p *projectState) inspectorReadsLocked(g *project.Graph, v *inspectorView, nodes []frontend.UpstreamNode, edges []frontend.UpstreamEdge) (map[project.ActionID]frontend.DeferHint, []func()) {
	if !p.deferOnLocked() {
		return nil, nil
	}
	v.Defer = true
	reads := map[project.ActionID]frontend.DeferHint{}
	var starts []func()
	asked := map[project.ActionID]bool{}
	for _, e := range edges {
		if asked[e.From] {
			continue
		}
		asked[e.From] = true
		a := g.Actions[e.From]
		if a == nil {
			continue
		}
		ready, s := p.deferStartsLocked([]*project.Action{a})
		starts = append(starts, s...)
		k := p.def.byAction[a.ID]
		if !ready || k == nil {
			v.DeferLoading = true
			continue
		}
		if k.d == nil {
			v.DeferError = cmpOr(p.def.err, "not worked out")
			continue
		}
		_, hints := frontend.DeferHints(g, a, k.d, p.deps.Now())
		for id, h := range hints {
			if _, ok := reads[id]; !ok {
				reads[id] = h
			}
		}
	}
	return reads, starts
}

// ---- the page's state ----

// inspectorState keeps the page's filters, ticks and depth for a model,
// leaving the rows' last runs, which the server keeps, as they are.
func (p *projectState) inspectorState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string                 `json:"key"`
		State session.InspectorState `json:"state"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Key == "" {
		http.Error(w, "bad request: key", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	if p.sess.Inspector == nil {
		p.sess.Inspector = map[string]session.InspectorState{}
	}
	old := p.sess.Inspector[req.Key]
	st := req.State
	st.Used = p.deps.Now()
	rows := map[string]session.InspectorRow{}
	for k, row := range st.Rows {
		row.Job = old.Rows[k].Job
		rows[k] = row
	}
	for k, row := range old.Rows { // a row the page doesn't show now keeps its run
		if _, ok := rows[k]; !ok && row.Job != nil {
			rows[k] = session.InspectorRow{Off: row.Off, Filter: row.Filter, Job: row.Job}
		}
	}
	st.Rows = rows
	st.Added = old.Added // inspectorAdded's
	p.sess.Inspector[req.Key] = st
	session.TrimInspected(p.sess.Inspector)
	p.mu.Unlock()
	writeJSON(w, okView{OK: true})
}

// inspectorAdded sets the tables added by hand to the page of the model
// key names: added, by their names before dev settings (a preset's), and
// with add, the action of that ID too, which must have a table.
func (p *projectState) inspectorAdded(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key   string           `json:"key"`
		Added []string         `json:"added"`
		Add   project.ActionID `json:"add"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Key == "" {
		http.Error(w, "bad request: key", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	added := slices.Clone(req.Added)
	if req.Add != "" {
		var a *project.Action
		if g := p.st.Graph; g != nil {
			a = g.Actions[req.Add]
		}
		switch {
		case a == nil:
			writeJSON(w, errorView{Error: "no such action: " + string(req.Add)})
			return
		case a.Kind == project.KindAssertion || a.Kind == project.KindTest:
			writeJSON(w, errorView{Error: a.Target.Name + " is " + a.Kind.String() + ": the inspector lists tables, as its walk does"})
			return
		case !frontend.QueryableTable(a):
			writeJSON(w, errorView{Error: a.Target.Name + " has no table to query"})
			return
		}
		if k := frontend.PresetKey(a); k != req.Key && !slices.Contains(added, k) {
			added = append(added, k)
		}
	}
	if p.sess.Inspector == nil {
		p.sess.Inspector = map[string]session.InspectorState{}
	}
	st := p.sess.Inspector[req.Key]
	st.Added, st.Used = added, p.deps.Now()
	p.sess.Inspector[req.Key] = st
	session.TrimInspected(p.sess.Inspector)
	writeJSON(w, okView{OK: true})
}

// ---- dry runs ----

// inspectorDryRun dry runs a row's query.
func (p *projectState) inspectorDryRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Table  string `json:"table"`
		Filter string `json:"filter"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v := scratchDryView{}
	v.WarnBytes, v.ConfirmBytes = p.thresholds()
	if p.deps.DryRun == nil {
		v.Error = "no dry runs here"
		writeJSON(w, v)
		return
	}
	if req.Table == "" {
		http.Error(w, "bad request: table", http.StatusBadRequest)
		return
	}
	def := p.scratchDefaults(r.Context())
	if def.Project == "" {
		v.Error = "no project to dry run in: set a quota project with gcloud"
		writeJSON(w, v)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), scratchDryTimeout)
	defer cancel()
	d, err := p.deps.DryRun(ctx, def.Project, def.Location, inspectorSQL(req.Table, req.Filter))
	if err != nil {
		v.Auth = errors.Is(err, bigquery.ErrAuth)
		v.Error = queryError(err).Error()
		writeJSON(w, v)
		return
	}
	v.OK, v.Bytes, v.Accuracy, v.StatementType = true, d.Bytes, d.Accuracy, d.StatementType
	v.Text = bigquery.FormatBytes(d.Bytes)
	if d.Accuracy == bigquery.AccuracyUnknown {
		v.Text = "unknown"
	} else {
		v.Cost = cost.FormatUSD(d.CostUSD())
	}
	writeJSON(w, v)
}

// ---- runs ----

type inspectorRunRequest struct {
	Key  string `json:"key"`
	Rows []struct {
		Key    string `json:"key"`
		Table  string `json:"table"`
		Filter string `json:"filter"`
	} `json:"rows"`
}

// inspectorRun queues the rows' queries. A row already queued or running
// is left to it.
func (p *projectState) inspectorRun(w http.ResponseWriter, r *http.Request) {
	var req inspectorRunRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Key == "" || len(req.Rows) == 0 {
		http.Error(w, "bad request: key and rows", http.StatusBadRequest)
		return
	}
	if _, err := p.bq(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	def := p.scratchDefaults(r.Context())
	if def.Project == "" {
		writeError(w, errors.New("no project to run in: set a quota project with gcloud"))
		return
	}
	p.mu.Lock()
	if p.insp.byModel == nil {
		p.insp.byModel = map[string]map[string]*inspectorRun{}
		p.insp.slots = make(chan struct{}, maxInspectorRuns)
	}
	runs := p.insp.byModel[req.Key]
	if runs == nil {
		runs = map[string]*inspectorRun{}
		p.insp.byModel[req.Key] = runs
	}
	var start []func()
	for _, row := range req.Rows {
		if row.Key == "" || row.Table == "" {
			continue
		}
		if old := runs[row.Key]; old != nil && (old.Phase == "queued" || old.Phase == "running") {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), inspectorRunLimit)
		run := &inspectorRun{Phase: "queued", SQL: inspectorSQL(row.Table, row.Filter), cancel: cancel}
		runs[row.Key] = run
		model, key := req.Key, row.Key
		start = append(start, func() { p.runInspected(ctx, def, model, key, run) })
	}
	v := maps.Clone(runs)
	p.mu.Unlock()
	for _, f := range start {
		go f()
	}
	p.hub.send("inspector", map[string]string{"key": req.Key})
	writeJSON(w, v)
}

// runInspected runs one row's query once a slot is free, follows it to
// the end and counts its rows, then keeps its job with the session.
func (p *projectState) runInspected(ctx context.Context, def scratchDefaultsView, model, key string, run *inspectorRun) {
	defer p.tellRunEnded() // xf web's auto-exit waits for these too
	defer run.cancel()
	changed := func(f func()) {
		p.mu.Lock()
		f()
		p.mu.Unlock()
		p.hub.send("inspector", map[string]string{"key": model})
	}
	end := func(phase, msg string) {
		changed(func() {
			run.Phase, run.Error, run.Ended = phase, msg, p.deps.Now()
			p.keepInspectedLocked(model, key, run)
		})
	}
	select {
	case p.insp.slots <- struct{}{}:
		defer func() { <-p.insp.slots }()
	case <-ctx.Done():
		end("cancelled", "")
		return
	}
	if ctx.Err() != nil {
		end("cancelled", "")
		return
	}
	c, err := p.bq(ctx)
	if err != nil {
		end("failed", err.Error())
		return
	}
	ref, err := c.StartQuery(ctx, bigquery.QueryOptions{Project: def.Project, Location: def.Location, SQL: run.SQL, Labels: map[string]string{inspectorLabel: "true"}})
	if err != nil {
		if ctx.Err() != nil {
			end("cancelled", "")
			return
		}
		end("failed", queryError(err).Error())
		return
	}
	changed(func() { run.Phase, run.Job = "running", &ref })
	wait := 500 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			cctx, cancel := context.WithTimeout(context.Background(), scratchCallLimit)
			_ = c.CancelQuery(cctx, ref)
			cancel()
			end("cancelled", "")
			return
		case <-time.After(wait):
		}
		wait = min(wait*3/2, 3*time.Second)
		st, err := c.QueryJob(ctx, ref)
		if err != nil {
			if ctx.Err() != nil {
				continue // cancelled: cancel the job above
			}
			end("failed", queryError(err).Error())
			return
		}
		if st.State != "DONE" {
			continue
		}
		if st.Error != "" {
			if st.Cancelled {
				end("cancelled", "")
			} else {
				end("failed", st.Error)
			}
			return
		}
		page, err := c.Rows(ctx, ref, "", 1)
		if err != nil {
			end("failed", rowsError(err).Error())
			return
		}
		changed(func() {
			run.Rows, run.Billed = page.Total, st.BytesBilled
			run.Cost = cost.FormatUSD(bigquery.CostUSD(st.BytesBilled))
		})
		end("done", "")
		return
	}
}

// keepInspectedLocked keeps a row's ended run as its last job in the
// session, so the page shows it again after xf web starts again.
func (p *projectState) keepInspectedLocked(model, key string, run *inspectorRun) {
	if run.Job == nil {
		return
	}
	if p.sess.Inspector == nil {
		p.sess.Inspector = map[string]session.InspectorState{}
	}
	st := p.sess.Inspector[model]
	if st.Rows == nil {
		st.Rows = map[string]session.InspectorRow{}
	}
	row := st.Rows[key]
	row.Job = &session.InspectorJob{Project: run.Job.Project, Location: run.Job.Location, ID: run.Job.ID, SQL: run.SQL,
		Rows: run.Rows, Error: run.Error, Ended: run.Ended}
	st.Rows[key] = row
	if st.Used.IsZero() {
		st.Used = p.deps.Now()
	}
	p.sess.Inspector[model] = st
	session.TrimInspected(p.sess.Inspector)
}

// inspectorCancel cancels a model's queued and running queries, or one
// row's when Row is set.
func (p *projectState) inspectorCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
		Row string `json:"row"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	n := 0
	p.mu.Lock()
	for k, run := range p.insp.byModel[req.Key] {
		if (req.Row == "" || k == req.Row) && (run.Phase == "queued" || run.Phase == "running") {
			run.cancel()
			n++
		}
	}
	p.mu.Unlock()
	writeJSON(w, map[string]int{"cancelled": n})
}

// inspectorRunning says whether any inspector query is queued or running,
// which holds off xf web's auto-exit as a run does.
func (p *projectState) inspectorRunningLocked() bool {
	for _, runs := range p.insp.byModel {
		for _, run := range runs {
			if run.Phase == "queued" || run.Phase == "running" {
				return true
			}
		}
	}
	return false
}

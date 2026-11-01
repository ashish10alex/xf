package web

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// deferGraph is a table reading two upstream tables in dev: customers,
// not built there, and payments.
func deferGraph() *project.Graph {
	cust := project.Target{Database: "dev", Schema: "s", Name: "customers"}
	pay := project.Target{Database: "dev", Schema: "s", Name: "payments"}
	orders := project.Target{Database: "dev", Schema: "s", Name: "orders"}
	return project.NewGraph([]*project.Action{
		{ID: cust.ID(), Target: cust, CanonicalTarget: cust, Kind: project.KindTable, FileName: "definitions/customers.sqlx", Query: "SELECT 1 AS id"},
		{ID: pay.ID(), Target: pay, CanonicalTarget: pay, Kind: project.KindTable, FileName: "definitions/payments.sqlx", Query: "SELECT 1 AS id"},
		{ID: orders.ID(), Target: orders, CanonicalTarget: orders, Kind: project.KindTable, FileName: "definitions/orders.sqlx",
			Query: "SELECT * FROM `dev.s.customers` JOIN `dev.s.payments` USING (id)", DependencyTargets: []project.Target{cust, pay}},
	})
}

// deferEngine is a fake engine with defer to prod on.
func deferEngine(g *project.Graph, on bool) *fakeEngine {
	f := &fakeEngine{states: make(chan engine.State, 4)}
	f.states <- engine.State{Graph: g, Compiles: 1, CompiledAt: time.Now(), Defer: on}
	return f
}

// fakeDeferral answers as the engine would with customers read from prod;
// stale marks it a Changed Action.
type fakeDeferral struct {
	mu    sync.Mutex
	stale bool
	reqs  []engine.DeferralRequest
}

func (f *fakeDeferral) ask(_ context.Context, req engine.DeferralRequest) (*engine.DeferralResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	prod := project.Target{Database: "prod", Schema: "s", Name: "customers"}
	d := &deferral.Deferral{
		Entries:    []deferral.Entry{{Dev: project.Target{Database: "dev", Schema: "s", Name: "customers"}, Prod: &prod, Status: deferral.Deferred, Stale: f.stale}},
		BuiltInDev: []deferral.BuiltInDev{{Dev: project.Target{Database: "dev", Schema: "s", Name: "payments"}}},
	}
	return &engine.DeferralResult{Deferral: d}, nil
}

func TestDeferRewritesWhatIsDryRunAndShown(t *testing.T) {
	var mu sync.Mutex
	var sqls []string
	dry := func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
		mu.Lock()
		sqls = append(sqls, sql)
		mu.Unlock()
		return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise, StatementType: "SELECT"}, nil
	}
	fd := &fakeDeferral{}
	_, ts, c := newTest(t, frontend.Deps{Engine: deferEngine(deferGraph(), true), DryRun: dry, Deferral: fd.ask, SetDefer: func(bool) {}})

	var v actionView
	waitFor(t, func() bool {
		v = actionView{}
		getJSON(t, c, ts.URL+"/api/action?id=dev.s.orders", &v)
		return v.Defer != nil && !v.Defer.Loading
	})
	if len(v.Defer.Rows) != 2 || v.Defer.Rows[0].Label != "→ prod" || v.Defer.Rows[0].Prod != "prod.s.customers" || v.Defer.Rows[1].Tone != "dev" {
		t.Fatalf("pane 2's list %+v", v.Defer.Rows)
	}
	if !strings.Contains(strings.Join(v.SQL.Plain, "\n"), "`prod.s.customers`") {
		t.Errorf("the SQL shown reads prod: %q", v.SQL.Plain)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.ContainsFunc(sqls, func(s string) bool { return strings.Contains(s, "`prod.s.customers`") })
	})
	mu.Lock()
	for _, s := range sqls {
		if strings.Contains(s, "`dev.s.customers`") {
			t.Errorf("dry run before the deferral was known: %q", s)
		}
	}
	mu.Unlock()
	if r := fd.reqs[0]; r.ProdCompilerOptions == nil || !slices.Contains(r.Actions, "dev.s.orders") {
		t.Errorf("request %+v", r)
	}
}

func TestAfterRefClose(t *testing.T) {
	for _, c := range []struct {
		src  string
		end  int
		want int
	}{
		{`FROM ${ref("a")} AS o`, 15, 16},
		{`from {{ ref('a') }} o`, 16, 19},
		{`FROM ${ref("a")`, 15, 15},
		{`x = ref("a") }`, 12, 12},
	} {
		if got := afterRefClose([]byte(c.src), c.end); got != c.want {
			t.Errorf("%q: %d, want %d", c.src, got, c.want)
		}
	}
}

func TestDeferOffDryRunsAsCompiled(t *testing.T) {
	fd := &fakeDeferral{}
	_, ts, c := newTest(t, frontend.Deps{Engine: deferEngine(deferGraph(), false), Deferral: fd.ask, SetDefer: func(bool) {}})
	var v actionView
	getJSON(t, c, ts.URL+"/api/action?id=dev.s.orders", &v)
	if v.Defer != nil || strings.Contains(strings.Join(v.SQL.Plain, "\n"), "prod.") {
		t.Errorf("defer off: %+v %q", v.Defer, v.SQL.Plain)
	}
}

func TestTurningDeferOnAsksForProdOptionsFirst(t *testing.T) {
	var set []bool
	deps := frontend.Deps{Engine: deferEngine(deferGraph(), false), Deferral: (&fakeDeferral{}).ask,
		SetDefer:        func(on bool) { set = append(set, on) },
		SaveProdOptions: func(context.Context, string) error { return nil }, Unset: config.KeyProdCompilerOptions}
	_, ts, c := newTest(t, deps)
	var a deferAnswer
	postJSON(t, c, ts.URL+"/api/defer", deferRequest{On: true}, &a)
	if !a.Prod || len(set) != 0 {
		t.Fatalf("the prompt goes first: %+v %v", a, set)
	}
	var ok okView
	postJSON(t, c, ts.URL+"/api/prod", map[string]any{"dismiss": true}, &ok)
	postJSON(t, c, ts.URL+"/api/defer", deferRequest{On: true}, &a)
	if !a.OK || !slices.Equal(set, []bool{true}) {
		t.Errorf("then it turns on: %+v %v", a, set)
	}
}

// fakeWriter records the Proxy Views made.
type fakeWriter struct {
	mu      sync.Mutex
	created []bigquery.ViewSpec
}

func (w *fakeWriter) View(context.Context, string, string, string) (*bigquery.ViewInfo, error) {
	return nil, nil
}

func (w *fakeWriter) CreateView(_ context.Context, v bigquery.ViewSpec) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.created = append(w.created, v)
	return nil
}
func (w *fakeWriter) UpdateView(context.Context, bigquery.ViewSpec) error { return nil }
func (w *fakeWriter) DeleteTable(context.Context, string, string, string) error {
	return nil
}
func (w *fakeWriter) ListTablesLabelled(context.Context, string, string) ([]bigquery.LabelledTable, error) {
	return nil, nil
}

func TestDeferredRunAsksAboutStaleThenMakesProxies(t *testing.T) {
	started := make(chan *fakeRun, 2)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx, opts: o}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done}, nil
	}
	fd := &fakeDeferral{stale: true}
	w := &fakeWriter{}
	_, ts, c := newTest(t, frontend.Deps{Engine: deferEngine(deferGraph(), true), StartRun: start, Deferral: fd.ask, SetDefer: func(bool) {},
		DeferWriter: func(context.Context) (deferral.Writer, error) { return w, nil },
		RunCommand:  func(o project.RunOptions) string { return "dataform run" }})
	waitFor(t, func() bool {
		var v actionView
		getJSON(t, c, ts.URL+"/api/action?id=dev.s.orders", &v)
		return v.Defer != nil && !v.Defer.Loading
	})

	req := runRequest{Kind: "actions", IDs: []project.ActionID{"dev.s.orders"}}
	var a runAnswer
	postJSON(t, c, ts.URL+"/api/run", req, &a)
	if a.OK || a.Ask == nil || !strings.Contains(a.Ask.Title, "changed on this branch") || a.Ask.Choices[0].ID != deferChanged {
		t.Fatalf("a Stale Deferral asks first: %+v", a)
	}
	if r := fd.reqs[len(fd.reqs)-1]; !r.Fresh || !r.Proxies {
		t.Errorf("a run looks again: %+v", r)
	}

	req.DeferOK = []string{deferStale}
	postJSON(t, c, ts.URL+"/api/run", req, &a)
	if !a.OK {
		t.Fatalf("run anyway: %+v", a)
	}
	f := <-started
	if len(w.created) != 1 || w.created[0].Table != "customers" || w.created[0].Query != "SELECT * FROM `prod.s.customers`" {
		t.Errorf("proxy views %+v", w.created)
	}
	if !slices.Equal(f.opts.Deferred, []string{"dev.s.customers → prod.s.customers"}) {
		t.Errorf("the run keeps what it read from prod: %v", f.opts.Deferred)
	}
	close(f.lines)
	f.done <- project.RunResult{}
}

func TestBuildChangedFirstAddsThemToTheRun(t *testing.T) {
	started := make(chan *fakeRun, 1)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx, opts: o}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done}, nil
	}
	fd := &fakeDeferral{stale: true}
	_, ts, c := newTest(t, frontend.Deps{Engine: deferEngine(deferGraph(), true), StartRun: start, Deferral: func(ctx context.Context, req engine.DeferralRequest) (*engine.DeferralResult, error) {
		if slices.Contains(req.Actions, "dev.s.customers") {
			return &engine.DeferralResult{Deferral: &deferral.Deferral{}}, nil // built in the run now
		}
		return fd.ask(ctx, req)
	}, SetDefer: func(bool) {}, DeferWriter: func(context.Context) (deferral.Writer, error) { return &fakeWriter{}, nil },
		RunCommand: func(o project.RunOptions) string { return "dataform run" }})
	var a runAnswer
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"dev.s.orders"}, DeferOK: []string{deferChanged}}, &a)
	if !a.OK {
		t.Fatalf("build changed first: %+v", a)
	}
	f := <-started
	if !slices.Contains(f.opts.Actions, "dev.s.customers") || !slices.Contains(f.opts.Actions, "dev.s.orders") {
		t.Errorf("the changed upstream table is added to the run: %v", f.opts.Actions)
	}
	close(f.lines)
	f.done <- project.RunResult{}
}

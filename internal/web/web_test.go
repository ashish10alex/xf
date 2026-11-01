package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/commanduse"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
	"github.com/ashish10alex/xf/internal/webtheme"
)

// fakeEngine reports one state, the graph compiled, and records what it
// is asked.
type fakeEngine struct {
	mu         sync.Mutex
	states     chan engine.State
	recompiles int
	focused    []project.ActionID
	themes     []string
}

func newFakeEngine(g *project.Graph) *fakeEngine {
	f := &fakeEngine{states: make(chan engine.State, 4)}
	f.states <- engine.State{Graph: g, Compiles: 1, CompiledAt: time.Now()}
	return f
}

func (f *fakeEngine) Subscribe() (<-chan engine.State, func()) { return f.states, func() {} }

func (f *fakeEngine) Recompile(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recompiles++
}

func (f *fakeEngine) Focus(id project.ActionID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focused = append(f.focused, id)
}

func (f *fakeEngine) SetTheme(theme string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.themes = append(f.themes, theme)
}

// testGraph is a table reading a declaration, with an assertion generated
// from its config, and a view in another file.
func testGraph() *project.Graph {
	raw := project.Target{Database: "p", Schema: "raw", Name: "orders"}
	orders := project.Target{Database: "p", Schema: "mart", Name: "orders"}
	return project.NewGraph([]*project.Action{
		{ID: raw.ID(), Target: raw, Kind: project.KindDeclaration, FileName: "definitions/sources.js"},
		{ID: orders.ID(), Target: orders, Kind: project.KindTable, FileName: "definitions/orders.sqlx",
			Query: "SELECT id\nFROM `p.raw.orders`", DependencyTargets: []project.Target{raw}, Tags: []string{"daily"}},
		{ID: "p.mart.orders_assertions_uniqueKey_0", Target: project.Target{Database: "p", Schema: "mart", Name: "orders_assertions_uniqueKey_0"},
			Kind: project.KindAssertion, FileName: "definitions/orders.sqlx", ParentAction: &orders,
			Query: "SELECT 1", DependencyTargets: []project.Target{orders}},
		{ID: "p.mart.customers", Target: project.Target{Database: "p", Schema: "mart", Name: "customers"}, Kind: project.KindView,
			FileName: "definitions/customers.sqlx", Query: "SELECT 1 AS id"},
	})
}

// newTest serves deps over HTTP, with a client that carries the token.
func newTest(t *testing.T, deps frontend.Deps) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	return newTestProject(t, &Project{Deps: deps})
}

// newTestProject serves p, as newTest does.
func newTestProject(t *testing.T, p *Project) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	return newTestConfig(t, Config{Project: p})
}

// newTestConfig serves cfg's project, as newTest does.
func newTestConfig(t *testing.T, cfg Config) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	if cfg.Project.Deps.Root == "" {
		cfg.Project.Deps.Root = "/work/proj"
	}
	cfg.Token = "secret"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		ts.Close()
		s.Close()
	})
	c := ts.Client()
	jar := &tokenJar{}
	c.Jar = jar
	jar.set(ts.URL)
	// Wait until the engine's state is in.
	deadline := time.Now().Add(5 * time.Second)
	for s.only().graph() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the engine's state never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s, ts, c
}

// tokenJar sends the token cookie to the test server.
type tokenJar struct {
	mu sync.Mutex
	u  *url.URL
}

func (j *tokenJar) set(raw string) { j.u, _ = url.Parse(raw) }

func (j *tokenJar) SetCookies(*url.URL, []*http.Cookie) {}

func (j *tokenJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.u == nil || u.Host != j.u.Host {
		return nil
	}
	return []*http.Cookie{{Name: cookieName, Value: "secret"}}
}

func getJSON(t *testing.T, c *http.Client, u string, v any) {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", u, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, c *http.Client, u string, body, v any) {
	t.Helper()
	b, _ := json.Marshal(body)
	res, err := c.Post(u, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: %s", u, res.Status)
	}
	if v != nil {
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
}

// On loopback alone a request needs no token, only this server's host and
// origin, so a reload or a restart keeps working; another site's requests
// are turned away by the headers browsers send.
func TestGuard(t *testing.T) {
	_, ts, _ := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, headers ...string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		for i := 0; i+1 < len(headers); i += 2 {
			if headers[i] == "Host" {
				req.Host = headers[i+1]
			} else {
				req.Header.Set(headers[i], headers[i+1])
			}
		}
		res, err := plain.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res
	}
	for _, c := range []struct {
		name, method, path string
		headers            []string
		want               int
	}{
		{"no token", "GET", "/api/boot", nil, http.StatusOK},
		{"an old tab's token", "GET", "/?token=stale", nil, http.StatusOK},
		{"its own page", "GET", "/api/boot", []string{"Sec-Fetch-Site", "same-origin", "Origin", ts.URL}, http.StatusOK},
		{"typed in", "GET", "/", []string{"Sec-Fetch-Site", "none", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, http.StatusOK},
		{"a link from another site", "GET", "/", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, http.StatusOK},
		{"another site's link to the API", "GET", "/api/boot", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, http.StatusForbidden},
		{"another site's frame", "GET", "/", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "iframe"}, http.StatusForbidden},
		{"another site's script", "GET", "/api/boot", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "script"}, http.StatusForbidden},
		{"another port on this machine", "GET", "/api/boot", []string{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "cors"}, http.StatusForbidden},
		{"another site's form", "POST", "/api/quit", []string{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, http.StatusForbidden},
		{"another host", "GET", "/api/boot", []string{"Host", "evil.example:80"}, http.StatusForbidden},
		{"another origin", "GET", "/api/boot", []string{"Origin", "http://evil.example"}, http.StatusForbidden},
		{"a proxy, no token", "GET", "/api/boot", []string{"X-Forwarded-For", "100.64.0.1"}, http.StatusUnauthorized},
		{"a proxy (Forwarded), no token", "GET", "/api/boot", []string{"Forwarded", "for=100.64.0.1"}, http.StatusUnauthorized},
	} {
		if res := do(c.method, c.path, c.headers...); res.StatusCode != c.want {
			t.Errorf("%s: %s, want %d", c.name, res.Status, c.want)
		}
	}
	res := do("GET", "/?token=secret", "X-Forwarded-For", "100.64.0.1")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Errorf("a proxy with the token: %s to %q, want 303 to /", res.Status, res.Header.Get("Location"))
	}
	if res := do("GET", "/api/boot", "X-Forwarded-For", "100.64.0.1", "Cookie", cookieName+"=secret"); res.StatusCode != http.StatusOK {
		t.Errorf("a proxy with the cookie: %s, want 200", res.Status)
	}
}

// The URL xf prints carries the token only when the server needs it.
func TestURL(t *testing.T) {
	l, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	local, _ := New(Config{Token: "secret"})
	defer local.Close()
	if u := local.URL(l); u != "http://"+l.Addr().String()+"/" {
		t.Errorf("loopback URL = %q, want no token", u)
	}
	remote, _ := New(Config{Token: "secret", Hosts: []string{"mac.tail1.ts.net"}})
	defer remote.Close()
	if u := remote.URL(l); !strings.HasSuffix(u, "/?token=secret") {
		t.Errorf("--allow-host URL = %q, want the token", u)
	}
}

func TestLocalHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:8080": true, "localhost:1": true, "[::1]:9": true, "app.localhost": true,
		"example.com": false, "10.0.0.1:80": false, "127.0.0.1.nip.io:80": false,
	} {
		if got := localHost(host); got != want {
			t.Errorf("localHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestFindAndAction(t *testing.T) {
	eng := newFakeEngine(testGraph())
	run := func(context.Context, project.RunOptions) (*project.Run, error) { return nil, nil }
	_, ts, c := newTest(t, frontend.Deps{Engine: eng, StartRun: run,
		RunCommand: func(o project.RunOptions) string { return "dataform run " + strings.Join(idStrings(o.Actions), " ") }})

	var found struct {
		Items []actionRef `json:"items"`
		Found int         `json:"found"`
		Total int         `json:"total"`
	}
	postJSON(t, c, ts.URL+"/api/find", map[string]any{"query": "mart orders"}, &found)
	if found.Total != 4 || found.Found != 2 || found.Items[0].ID != "p.mart.orders" {
		t.Errorf("find mart orders = %+v, want p.mart.orders first of 2 of 4", found)
	}
	postJSON(t, c, ts.URL+"/api/find", map[string]any{"query": "$view"}, &found)
	if len(found.Items) != 1 || found.Items[0].ID != "p.mart.customers" {
		t.Errorf("find $view = %+v, want the view", found.Items)
	}

	var v actionView
	getJSON(t, c, ts.URL+"/api/action?id=p.mart.orders&deps=true", &v)
	if len(v.Pane) != 2 || v.Pane[0].ID != "p.mart.orders" || v.Pane[1].Kind != "assertion" {
		t.Errorf("pane = %+v, want the table and its assertion", v.Pane)
	}
	if len(v.Lineage.Up) != 1 || v.Lineage.Up[0].ID != "p.raw.orders" || !v.Lineage.Up[0].External {
		t.Errorf("upstream = %+v, want the declaration", v.Lineage.Up)
	}
	if len(v.Lineage.Down) != 1 || v.Lineage.Down[0].Kind != "assertion" {
		t.Errorf("downstream = %+v, want the assertion", v.Lineage.Down)
	}
	if strings.Join(v.SQL.Plain, "\n") != "-- query\nSELECT id\nFROM `p.raw.orders`" || len(v.SQL.Lines) != 3 {
		t.Errorf("sql = %q, %d token lines", v.SQL.Plain, len(v.SQL.Lines))
	}
	if v.Run.Label != "2 actions" || v.Run.Command != "$ dataform run p.mart.orders p.mart.orders_assertions_uniqueKey_0" {
		t.Errorf("run = %+v", v.Run)
	}
	if v.FileAbs == "" || v.ConsoleURL == "" {
		t.Errorf("fileAbs %q, consoleUrl %q: want both", v.FileAbs, v.ConsoleURL)
	}
	eng.mu.Lock()
	focused := eng.focused
	eng.mu.Unlock()
	if len(focused) != 1 || focused[0] != "p.mart.orders" {
		t.Errorf("focused %v, want the action compiled ahead", focused)
	}

	// An edit to its config that renamed it: the action its file defines
	// now, by name. With no file to follow, there is no such action.
	getJSON(t, c, ts.URL+"/api/action?id=p.old_mart.orders&file=definitions/orders.sqlx", &v)
	if v.ID != "p.mart.orders" {
		t.Errorf("renamed: got %s, want p.mart.orders", v.ID)
	}
	if res, err := c.Get(ts.URL + "/api/action?id=p.old_mart.orders"); err != nil {
		t.Fatal(err)
	} else if res.Body.Close(); res.StatusCode != http.StatusNotFound {
		t.Errorf("gone, no file: %s, want 404", res.Status)
	}

	// With selected only, r runs just the table.
	getJSON(t, c, ts.URL+"/api/action?id=p.mart.orders&selectedOnly=true", &v)
	if v.Run.Label != "mart.orders" || v.Run.Name != "orders" {
		t.Errorf("selected only: run = %+v", v.Run)
	}
	// A declaration can't run.
	getJSON(t, c, ts.URL+"/api/action?id=p.raw.orders", &v)
	if len(v.Run.Actions) != 0 || v.Run.Why != "declarations can't be run" || !v.SQL.Empty {
		t.Errorf("declaration: run %+v, sql %+v", v.Run, v.SQL)
	}
	// Alone in its pane, it has ] and [ say so.
	if len(v.Pane) != 1 || v.Alone != "no other actions in this file" {
		t.Errorf("declaration: pane %+v, alone %q", v.Pane, v.Alone)
	}
	res, err := c.Get(ts.URL + "/api/action?id=nope")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown action: %s, want 404", res.Status)
	}
}

func idStrings(ids []project.ActionID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// fakeRun is a run whose lines and end the test sends.
type fakeRun struct {
	lines chan string
	done  chan project.RunResult
	ctx   context.Context
	opts  project.RunOptions
}

func TestRunStreamsToEveryPage(t *testing.T) {
	started := make(chan *fakeRun, 1)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx, opts: o}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done}, nil
	}
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), StartRun: start,
		RunCommand: func(o project.RunOptions) string { return "dataform run" }})

	events := openEvents(t, c, ts.URL, s.startKey)
	var ok okView
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"p.mart.orders"},
		Toggles: runToggles{Deps: true}}, &ok)
	if !ok.OK {
		t.Fatalf("run: %+v", ok)
	}
	f := <-started
	if !f.opts.IncludeDeps || len(f.opts.Actions) != 1 {
		t.Errorf("run options = %+v, want the table with its deps", f.opts)
	}
	// A second run waits for the first.
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"p.mart.orders"}}, &ok)
	if ok.OK || !strings.Contains(ok.Message, "already in progress") {
		t.Errorf("second run: %+v, want refused", ok)
	}
	f.lines <- "\x1b[32mTable created:\x1b[0m mart.orders"
	close(f.lines)
	f.done <- project.RunResult{Duration: 1500 * time.Millisecond}

	if line := events.until(t, "log", "Table created"); strings.Contains(line, "\\u001b") {
		t.Errorf("log line keeps its escape codes: %s", line)
	}
	events.until(t, "run", `"running":false`)

	var v runView
	getJSON(t, c, ts.URL+"/api/run", &v)
	var texts []string
	for _, l := range v.Lines {
		texts = append(texts, l.Text)
	}
	want := []string{"$ dataform run", "Table created: mart.orders", "✓ exit 0 · 1.5s"}
	if strings.Join(texts, "\n") != strings.Join(want, "\n") || v.Running || v.Title != "✓ exit 0 · 1.5s" || v.Label != "mart.orders" {
		t.Errorf("run = %+v, lines %q, want %q", v, texts, want)
	}
}

// A run may build the tables a failed dry run read, so it is tried again,
// the engine having forgotten its own failure first.
func TestRunRetriesFailedDryRuns(t *testing.T) {
	var built, forgot atomic.Bool
	dry := func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
		if !built.Load() {
			return nil, &bigquery.APIError{Status: 400, Message: "Unrecognized name: id at [1:8]"}
		}
		return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise, StatementType: "SELECT"}, nil
	}
	started := make(chan *fakeRun, 1)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx, opts: o}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done}, nil
	}
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), DryRun: dry, StartRun: start,
		ForgetFailedDryRuns: func() { forgot.Store(true) },
		RunCommand:          func(o project.RunOptions) string { return "dataform run" }})

	dryLine := func() string {
		var v actionView
		getJSON(t, c, ts.URL+"/api/action?id=p.mart.orders", &v)
		for _, a := range v.Pane {
			if a.ID == "p.mart.orders" {
				for _, d := range a.Details {
					if d.Dry {
						return d.Value
					}
				}
			}
		}
		return ""
	}
	waitFor(t, func() bool { return strings.Contains(dryLine(), "Unrecognized name") })

	var ok okView
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"p.mart.customers"}}, &ok)
	if !ok.OK {
		t.Fatalf("run: %+v", ok)
	}
	f := <-started
	built.Store(true)
	if !strings.Contains(dryLine(), "Unrecognized name") {
		t.Fatal("the failed dry run was tried again before the run ended")
	}
	close(f.lines)
	f.done <- project.RunResult{}
	waitFor(t, forgot.Load)
	waitFor(t, func() bool { return strings.Contains(dryLine(), "MiB") })
}

// A dry run's answer older than engine.DryRunMaxAge is asked again: the
// e overlay asks again for its old failures, so one fixed since (a table
// built outside xf) leaves it and the status line.
func TestOldDryRunFailuresAskedAgain(t *testing.T) {
	var fixed atomic.Bool
	dry := func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
		if strings.Contains(sql, "raw.orders") && !fixed.Load() {
			return nil, &bigquery.APIError{Status: 404, Message: "Not found: Table p:raw.orders\nwas not found"}
		}
		return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise, StatementType: "SELECT"}, nil
	}
	var mu sync.Mutex
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), DryRun: dry, Now: clock})
	byFile := func() map[string]fileDryRun {
		var b bootView
		getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &b)
		return b.Project.State.DryRunByFile
	}
	getJSON(t, c, ts.URL+"/api/action?id=p.mart.orders", &actionView{})
	waitFor(t, func() bool { return byFile()["definitions/orders.sqlx"].Count == 1 })
	if f, want := byFile()["definitions/orders.sqlx"], (fileDryRun{Count: 1, Name: "mart.orders", Msg: "Not found: Table p:raw.orders"}); f != want {
		t.Errorf("orders.sqlx = %+v, want %+v", f, want)
	}

	fixed.Store(true)
	getJSON(t, c, ts.URL+"/api/errors", &struct{}{})
	time.Sleep(50 * time.Millisecond)
	if len(byFile()) != 1 {
		t.Fatal("a fresh failure was asked again")
	}
	mu.Lock()
	now = now.Add(engine.DryRunMaxAge)
	mu.Unlock()
	getJSON(t, c, ts.URL+"/api/errors", &struct{}{})
	waitFor(t, func() bool { return len(byFile()) == 0 })
}

func TestRunFailedSaysHowToRunInTerminal(t *testing.T) {
	started := make(chan *fakeRun, 1)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx, opts: o}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done}, nil
	}
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), StartRun: start, Root: "/work/my project",
		RunCommand: func(o project.RunOptions) string { return "dbt build --select orders" }})
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"p.mart.orders"}}, nil)
	f := <-started
	if !f.opts.Color {
		t.Error("a local run from the page didn't ask for colour")
	}
	f.lines <- "\x1b[31mERROR\x1b[0m creating orders"
	close(f.lines)
	f.done <- project.RunResult{ExitCode: 1, Duration: time.Second}
	var v runView
	waitFor(t, func() bool {
		getJSON(t, c, ts.URL+"/api/run", &v)
		return !v.Running && v.Title != "running…"
	})
	cmd := cmdrun.ShellJoin([]string{"cd", "/work/my project"}) + " && dbt build --select orders"
	if v.Command != cmd {
		t.Errorf("command = %q, want %q", v.Command, cmd)
	}
	if got := v.Lines[1]; got.Text != "ERROR creating orders" || len(got.Spans) != 2 || got.Spans[0].Class != "a-red" {
		t.Errorf("output line = %+v, want ERROR in red", got)
	}
	if last := v.Lines[len(v.Lines)-1]; last.Copy != cmd || !strings.Contains(last.Text, "terminal") {
		t.Errorf("last line = %+v, want how to run it in a terminal", last)
	}
}

// A failed run whose log has no known fix offers none.
func TestRunFailedOffersNoFixOfItsOwn(t *testing.T) {
	started := make(chan *fakeRun, 1)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx, opts: o}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done}, nil
	}
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), StartRun: start, Root: t.TempDir(),
		RunCommand: func(o project.RunOptions) string { return "dataform run" }})
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"p.mart.orders"}}, nil)
	f := <-started
	f.lines <- "Error: Access Denied"
	close(f.lines)
	f.done <- project.RunResult{ExitCode: 1, Duration: time.Second}
	var v runView
	waitFor(t, func() bool {
		getJSON(t, c, ts.URL+"/api/run", &v)
		return !v.Running && v.Title != "running…"
	})
	for _, l := range v.Lines {
		if l.Fix != nil {
			t.Errorf("line %+v offers a fix", l)
		}
	}
}

func TestRunCancel(t *testing.T) {
	started := make(chan *fakeRun, 1)
	killed := make(chan bool, 1)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done, Kill: func() { killed <- true }}, nil
	}
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), StartRun: start})
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"p.mart.orders"}}, nil)
	f := <-started
	waitFor(t, func() bool {
		var v runView
		getJSON(t, c, ts.URL+"/api/run", &v)
		return v.Running
	})
	postJSON(t, c, ts.URL+"/api/run/cancel", nil, nil)
	select {
	case <-f.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the first cancel didn't cancel the run's context")
	}
	postJSON(t, c, ts.URL+"/api/run/cancel", nil, nil)
	select {
	case <-killed:
	case <-time.After(5 * time.Second):
		t.Fatal("the second cancel didn't kill the run")
	}
	close(f.lines)
	f.done <- project.RunResult{Canceled: true, Duration: time.Second}
	waitFor(t, func() bool {
		var v runView
		getJSON(t, c, ts.URL+"/api/run", &v)
		return !v.Running && v.Title == "cancelled · 1.0s"
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCompileAndSession(t *testing.T) {
	eng := newFakeEngine(testGraph())
	s, ts, c := newTest(t, frontend.Deps{Engine: eng})
	postJSON(t, c, ts.URL+"/api/compile", nil, nil)
	eng.mu.Lock()
	n := eng.recompiles
	eng.mu.Unlock()
	if n != 1 {
		t.Errorf("recompiles = %d, want 1", n)
	}
	postJSON(t, c, ts.URL+"/api/session", resumeView{Selected: "p.mart.orders", Recent: []project.ActionID{"p.mart.orders"}}, nil)
	var b bootView
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &b)
	if b.Project == nil || b.Project.Resume.Selected != "p.mart.orders" || !b.Project.State.HasGraph || b.Project.State.Actions != 4 {
		t.Errorf("boot = %+v", b.Project)
	}
}

func TestGitLine(t *testing.T) {
	git := func(context.Context) (dataformapi.GitState, error) {
		return dataformapi.GitState{Branch: "feat", RemoteBranch: true, Dirty: []string{"definitions/a.sqlx"}}, nil
	}
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), APIGit: git})
	var v gitView
	getJSON(t, c, ts.URL+"/api/git", &v)
	text := func(parts []frontend.GitPart) string {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	if got, want := text(v.Run), "runs origin/feat · 1 uncommitted file won't run"; got != want {
		t.Errorf("run line = %q, want %q", got, want)
	}
	if got, want := text(v.Check), "checks origin/feat · 1 uncommitted file won't be checked"; got != want {
		t.Errorf("check line = %q, want %q", got, want)
	}
	if d := v.Run[3].Detail; !slices.Equal(d, []string{"definitions/a.sqlx"}) {
		t.Errorf("uncommitted detail = %q", d)
	}
}

func TestSQLHighlight(t *testing.T) {
	a := &project.Action{Kind: project.KindTable, Query: "SELECT 1\n\tFROM t\r"}
	v := sqlOf(a, false)
	if strings.Join(v.Plain, "\n") != "-- query\nSELECT 1\n    FROM t" {
		t.Errorf("plain = %q", v.Plain)
	}
	if len(v.Lines) != len(v.Plain) {
		t.Fatalf("%d token lines for %d lines", len(v.Lines), len(v.Plain))
	}
	if tok := v.Lines[1][0]; tok[1] != "SELECT" || !strings.HasPrefix(tok[0], "k") {
		t.Errorf("first token = %q, want SELECT as a keyword", tok)
	}
	for i, l := range v.Lines {
		var b strings.Builder
		for _, tok := range l {
			b.WriteString(tok[1])
		}
		if b.String() != v.Plain[i] {
			t.Errorf("line %d: tokens %q, plain %q", i, b.String(), v.Plain[i])
		}
	}
}

// Outside a project the page gets the picker, its paths from home as ~.
func TestPickerBoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	inHome := filepath.Join(home, "repos", "shop")
	s, err := New(Config{Token: "secret", Projects: func() []session.Project {
		return []session.Project{
			{Root: inHome, Backend: "dbt", LastVisited: time.Now()},
			{Root: filepath.FromSlash("/srv/other")},
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar
	var b bootView
	getJSON(t, c, ts.URL+"/api/boot", &b)
	if b.Project != nil || len(b.Picker) != 2 {
		t.Fatalf("boot = %+v, want the picker with 2 projects", b)
	}
	want := "~" + string(filepath.Separator) + filepath.Join("repos", "shop")
	if p := b.Picker[0]; p.Path != want || p.Root != inHome || p.Name != "shop" || p.Backend != "dbt" || p.When != "just now" {
		t.Errorf("first = %+v, want path %q", p, want)
	}
	if p := b.Picker[1]; p.Path != p.Root || p.Backend != "dataform" {
		t.Errorf("outside home = %+v, want its whole path and dataform", p)
	}
	// Every word must be in the tool or the path.
	for q, want := range map[string]int{"SHOP": 1, "dbt repos": 1, "srv": 1, "dbt srv": 0, "": 2} {
		var b bootView
		getJSON(t, c, ts.URL+"/api/boot?q="+url.QueryEscape(q), &b)
		if len(b.Picker) != want {
			t.Errorf("q %q: %d projects, want %d", q, len(b.Picker), want)
		}
	}
}

// A path in the picker's search lists the folder and those it completes
// to, and opens one that is a project's root as that tool's, even one xf
// has not opened before; a remembered one opens as it was remembered.
func TestPickerPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	mk := func(rel string) string {
		p := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return filepath.Dir(p)
	}
	shop := mk("repos/shop/dbt_project.yml")
	both := mk("repos/both/dbt_project.yml")
	mk("repos/both/workflow_settings.yaml")
	plain := mk("repos/plain/x")
	var opened []session.Project
	s, err := New(Config{Token: "secret",
		Projects: func() []session.Project { return []session.Project{{Root: shop, Backend: "dbt"}} },
		Open: func(p session.Project) (*Project, error) {
			opened = append(opened, p)
			return &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), Root: p.Root}}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar

	var b bootView
	getJSON(t, c, ts.URL+"/api/boot?q="+url.QueryEscape("~/repos/"), &b)
	var got []string
	for _, r := range b.Paths {
		got = append(got, r.Path+" "+r.Backend)
	}
	want := []string{"~/repos/ ", "~/repos/both dataform", "~/repos/both dbt", "~/repos/shop dbt", "~/repos/plain "}
	if !slices.Equal(got, want) || b.Total != 1 || len(b.Picker) != 0 {
		t.Fatalf("paths = %q (total %d), want %q", got, b.Total, want)
	}

	pick := func(root, tool string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"root": root, "backend": tool})
		res, err := c.Post(ts.URL+"/api/pick", "application/json", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if code := pick(plain, "dbt"); code != http.StatusUnprocessableEntity {
		t.Errorf("a folder of no project: %d, want 422", code)
	}
	if code := pick("repos/both", "dbt"); code != http.StatusNotFound {
		t.Errorf("a relative path: %d, want 404", code)
	}
	if code := pick(both, "dbt"); code != http.StatusOK || len(opened) != 1 || (opened[0].Root != both || opened[0].Backend != "dbt") {
		t.Fatalf("both as dbt: %d, opened %+v", code, opened)
	}
	postJSON(t, c, ts.URL+"/api/close", nil, nil)
	if code := pick(shop+string(filepath.Separator), "dbt"); code != http.StatusOK || opened[1].Root != shop {
		t.Errorf("a remembered project by another spelling: %d, opened %+v, want it as remembered", code, opened[1])
	}
}

// --allow-host lets a request name another host, such as a Tailscale
// name proxying to xf web, but not without the token, which every request
// then needs, a loopback one too.
func TestAllowedHost(t *testing.T) {
	s, err := New(Config{Token: "secret", Hosts: []string{"mac.tail1.ts.net"}})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, c := range []struct {
		host, origin string
		cookie       bool
		want         int
	}{
		{"mac.tail1.ts.net", "", true, http.StatusOK},
		{"MAC.tail1.ts.net.:443", "https://MAC.tail1.ts.net.:443", true, http.StatusOK},
		{"mac.tail1.ts.net", "", false, http.StatusUnauthorized},
		{"other.tail1.ts.net", "", true, http.StatusForbidden},
		{"mac.tail1.ts.net", "https://evil.example", true, http.StatusForbidden},
		{"127.0.0.1:8080", "", false, http.StatusUnauthorized},
		{"127.0.0.1:8080", "", true, http.StatusOK},
	} {
		req := httptest.NewRequest("GET", "/api/boot", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.cookie {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: "secret"})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("host %q origin %q cookie %v: %d, want %d", c.host, c.origin, c.cookie, rec.Code, c.want)
		}
	}
}

// Projects open side by side, each named by its key, and stay open with
// no page on them until one is closed (ADR 0008): closing tells its pages,
// with the projects still open (the last shown first), saves its session
// and lets go of its engine. A page naming a closed one opens it again.
func TestProjectsOpenSideBySide(t *testing.T) {
	var mu sync.Mutex
	var saved []session.Session
	closed := map[string]int{}
	opened := func(root string) *Project {
		return &Project{
			Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), Root: root, Resume: session.Session{Selected: "p.mart.orders"}},
			SaveSession: func(s session.Session) {
				mu.Lock()
				defer mu.Unlock()
				saved = append(saved, s)
			},
			Close: func() {
				mu.Lock()
				defer mu.Unlock()
				closed[root]++
			},
		}
	}
	s, err := New(Config{Token: "secret", Project: opened("/work/proj"), AutoExit: func() time.Duration { return 50 * time.Millisecond },
		Projects: func() []session.Project { return []session.Project{{Root: "/work/proj"}, {Root: "/work/other"}} },
		Open:     func(p session.Project) (*Project, error) { return opened(p.Root), nil }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	if s.startKey != "proj" {
		t.Fatalf("start key %q, want proj", s.startKey)
	}

	// Another opens beside it; the projects list marks both open.
	var picked struct{ Key string }
	postJSON(t, c, ts.URL+"/api/pick", map[string]string{"root": "/work/other"}, &picked)
	if picked.Key != "other" {
		t.Fatalf("pick answered %+v, want the key other", picked)
	}
	var b bootView
	getJSON(t, c, ts.URL+"/api/boot?project=other", &b)
	if b.Project == nil || b.Project.Root != "/work/other" || b.Key != "other" {
		t.Fatalf("boot of other: %+v", b)
	}
	var list bootView
	getJSON(t, c, ts.URL+"/api/boot", &list)
	if list.Project != nil || len(list.Picker) != 2 || !list.Picker[0].Open || list.Picker[0].Key != "proj" || !list.Picker[1].Open || list.Picker[1].Key != "other" {
		t.Fatalf("the picker: %+v, want proj and other open", list.Picker)
	}

	// With no page on either for longer than the auto-exit wait, both
	// stay open.
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	if len(closed) != 0 {
		t.Errorf("closed %v with no page on them", closed)
	}
	mu.Unlock()

	// Closing proj tells its pages, which go to other.
	page := openEvents(t, c, ts.URL, "proj")
	var left struct{ Open []string }
	postJSON(t, c, ts.URL+"/api/close", map[string]string{"key": "proj", "by": "Window 1"}, &left)
	if len(left.Open) != 1 || left.Open[0] != "other" {
		t.Errorf("close answered %+v, want other still open", left)
	}
	if ev := page.until(t, "closed", `"key":"proj"`); !strings.Contains(ev, `"by":"Window 1"`) || !strings.Contains(ev, `"open":["other"]`) {
		t.Errorf("closed event %s", ev)
	}
	mu.Lock()
	if closed["/work/proj"] != 1 || closed["/work/other"] != 0 || len(saved) != 1 || saved[0].Selected != "p.mart.orders" {
		t.Errorf("closed %v, saved %+v: want proj closed and its session saved", closed, saved)
	}
	mu.Unlock()

	// q on the one project left closes it.
	postJSON(t, c, ts.URL+"/api/close", nil, &left)
	if len(left.Open) != 0 || s.only() != nil {
		t.Errorf("after closing the last: %+v open", left)
	}

	// A page on a closed project opens it again; an unknown one says so.
	var again bootView
	getJSON(t, c, ts.URL+"/api/boot?project=proj", &again)
	if again.Project == nil || again.Project.Root != "/work/proj" {
		t.Fatalf("boot of closed proj: %+v, want it open again", again)
	}
	var nope bootView
	getJSON(t, c, ts.URL+"/api/boot?project=nope", &nope)
	if nope.Project != nil || nope.Missing == "" || len(nope.Picker) != 2 {
		t.Errorf("boot of an unknown project: project %v, missing %q, %d picker rows: want the picker saying so", nope.Project, nope.Missing, len(nope.Picker))
	}
}

// Two remembered projects of one name are told apart by their folders.
func TestProjectKeysOfOneName(t *testing.T) {
	s := &Server{open: map[string]*openProject{}}
	remembered := []session.Project{{Root: "/a/df"}, {Root: "/b/df"}, {Root: "/c/shop"}}
	if k := s.keyOf("/a/df", remembered, nil); k != "/a/df" {
		t.Errorf("key of /a/df = %q, want its folder", k)
	}
	if k := s.keyOf("/c/shop", remembered, nil); k != "shop" {
		t.Errorf("key of /c/shop = %q, want its name", k)
	}
	if pr, ok := s.rememberedByKey("/b/df", remembered, nil); !ok || pr.Root != "/b/df" {
		t.Errorf("/b/df named %+v", pr)
	}
	if _, ok := s.rememberedByKey("df", remembered, nil); ok {
		t.Error("an ambiguous name found a project")
	}
}

// o with a terminal editor asks for another, which is saved and then
// opens the file.
func TestChangeEditor(t *testing.T) {
	var mu sync.Mutex
	editor := "nvim"
	var savedTo []bool
	opened := 0
	deps := frontend.Deps{Engine: newFakeEngine(testGraph()), Root: "/work/proj", Info: &frontend.ProjectInfo{},
		Open: func(file string, line int) (frontend.Opening, error) {
			mu.Lock()
			defer mu.Unlock()
			if editor == "nvim" {
				return frontend.Opening{Leave: true}, nil
			}
			opened++
			return frontend.Opening{}, nil
		}}
	hook := &Editor{
		Current: func() (string, string, bool) {
			mu.Lock()
			defer mu.Unlock()
			return editor, "$EDITOR", editor != "nvim"
		},
		Choices: func() []EditorChoice { return []EditorChoice{{Command: "code", Name: "VS Code", Found: true}} },
		Set: func(command string, project bool) (string, error) {
			if command == "vim" {
				return "", errors.New("vim runs in a terminal")
			}
			mu.Lock()
			defer mu.Unlock()
			editor, savedTo = command, append(savedTo, project)
			return "/home/me/.config/xf/config.json", nil
		},
	}
	s, err := New(Config{Token: "secret", Project: &Project{Deps: deps, Editor: hook}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar
	waitFor(t, func() bool { return s.only().graph() != nil })

	var ok okView
	postJSON(t, c, ts.URL+"/api/open", map[string]string{"id": "p.mart.orders"}, &ok)
	if ok.OK || ok.Kind != "editor" {
		t.Fatalf("open with nvim = %+v, want kind editor", ok)
	}
	var v editorChoicesView
	getJSON(t, c, ts.URL+"/api/editor", &v)
	if v.Current != "nvim" || v.Usable || len(v.Choices) != 1 {
		t.Errorf("editor = %+v", v)
	}
	res, err := c.Post(ts.URL+"/api/editor", "application/json", strings.NewReader(`{"command":"vim"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a terminal editor: %s, want refused", res.Status)
	}
	postJSON(t, c, ts.URL+"/api/editor", map[string]any{"command": " code ", "open": "p.mart.orders"}, &ok)
	mu.Lock()
	defer mu.Unlock()
	if !ok.OK || editor != "code" || opened != 1 || len(savedTo) != 1 || savedTo[0] {
		t.Errorf("save = %+v, editor %q, opened %d, saved %v: want code saved for every project and the file open", ok, editor, opened, savedTo)
	}
	if deps.Info.EditorCommand != "code" {
		t.Errorf("project info still says %q", deps.Info.EditorCommand)
	}
}

func TestCommandUsesAreCountedAndBooted(t *testing.T) {
	uses := commanduse.Open(filepath.Join(t.TempDir(), "commands.json"))
	if _, err := uses.Add("Run"); err != nil { // as the TUI would
		t.Fatal(err)
	}
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), CommandUses: uses})
	var boot struct {
		Project struct {
			CommandUses map[string]commanduse.Use `json:"commandUses"`
		} `json:"project"`
	}
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &boot)
	if boot.Project.CommandUses["Run"].Count != 1 {
		t.Errorf("boot uses %v", boot.Project.CommandUses)
	}
	var got map[string]commanduse.Use
	postJSON(t, c, ts.URL+"/api/command", map[string]string{"title": "Check: compile"}, &got)
	if got["Run"].Count != 1 || got["Check: compile"].Count != 1 || uses.Load()["Check: compile"].Count != 1 {
		t.Errorf("after a use: answered %v, kept %v", got, uses.Load())
	}
}

func TestThemeIsSavedAndSent(t *testing.T) {
	eng := newFakeEngine(testGraph())
	var saved []string
	s, err := New(Config{Project: &Project{Deps: frontend.Deps{Engine: eng, Root: "/work/proj"}}, Token: "secret",
		Theme:    func(string) webtheme.Choice { return webtheme.Choice{Theme: "light", Dark: "nord"} },
		SetTheme: func(root, key, id string) error { saved = append(saved, root+key+"="+id); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	c.Jar = jar
	jar.set(ts.URL)

	var boot struct {
		Theme  webtheme.Choice  `json:"theme"`
		Themes []webtheme.Theme `json:"themes"`
	}
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &boot)
	// light, as webTheme was before there were themes, is xf's own.
	if want := (webtheme.Choice{Theme: "xf-light", Light: "xf-light", Dark: "nord"}); boot.Theme != want {
		t.Errorf("boot theme %+v", boot.Theme)
	}
	if len(boot.Themes) != len(webtheme.All()) || boot.Themes[0].Kind != webtheme.Light {
		t.Errorf("boot themes %+v", boot.Themes)
	}
	postJSON(t, c, ts.URL+"/api/theme", map[string]string{"theme": "dracula"}, nil)
	postJSON(t, c, ts.URL+"/api/theme", map[string]string{"key": "webThemeLight", "theme": "catppuccin-latte"}, nil)
	eng.mu.Lock()
	engineGot := append([]string(nil), eng.themes...)
	eng.mu.Unlock()
	// The TUI keeps its own theme: the engine isn't told.
	if strings.Join(saved, ",") != "webTheme=dracula,webThemeLight=catppuccin-latte" || len(engineGot) != 0 {
		t.Errorf("saved %q, engine %q", saved, engineGot)
	}
	for _, body := range []string{`{"theme":"sepia"}`, `{"key":"webThemeDark","theme":"auto"}`, `{"key":"theme","theme":"dark"}`} {
		res, err := c.Post(ts.URL+"/api/theme", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %s", body, res.Status)
		}
	}
	res, err := c.Get(ts.URL + "/themes.css")
	if err != nil {
		t.Fatal(err)
	}
	css, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/css") || !strings.Contains(string(css), `[data-theme="dracula"] {`) {
		t.Errorf("themes.css: %s, %.80s", res.Header.Get("Content-Type"), css)
	}
}

// A project's theme is saved to its own file and sent to its pages alone;
// the user's goes to every page, which a project with its own ignores.
func TestThemeIsPerProject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".xf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, config.ProjectFile), []byte(`{"webTheme": "nord"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var saved []string
	s, err := New(Config{Project: &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), Root: root}}, Token: "secret",
		Theme: func(r string) webtheme.Choice {
			if r == "" {
				return webtheme.Choice{Theme: "dracula"}
			}
			return webtheme.Choice{Theme: "nord"}
		},
		SetTheme: func(r, key, id string) error {
			saved = append(saved, strings.TrimPrefix(r, root)+":"+key+"="+id)
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	c.Jar = jar
	jar.set(ts.URL)

	var boot struct {
		Theme    webtheme.Choice `json:"theme"`
		ThemeOwn []string        `json:"themeOwn"`
	}
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &boot)
	if boot.Theme.Theme != "nord" || strings.Join(boot.ThemeOwn, ",") != "webTheme" {
		t.Errorf("project boot %+v, own %q", boot.Theme, boot.ThemeOwn)
	}
	boot.Theme, boot.ThemeOwn = webtheme.Choice{}, nil
	getJSON(t, c, ts.URL+"/api/boot", &boot)
	if boot.Theme.Theme != "dracula" || len(boot.ThemeOwn) != 0 {
		t.Errorf("list boot %+v, own %q", boot.Theme, boot.ThemeOwn)
	}
	post := func(body map[string]string) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", ts.URL+"/api/theme", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(projectHeader, s.startKey)
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	for _, body := range []map[string]string{
		{"scope": "project", "theme": "monokai-pro-classic"},
		{"scope": "project", "key": "webThemeDark", "theme": "tokyo-night"},
		{"scope": "project", "theme": ""}, // the project's webTheme goes
		{"theme": "github-light"},
	} {
		if st := post(body); st != http.StatusOK {
			t.Errorf("%v: %d", body, st)
		}
	}
	if want := ":webTheme=monokai-pro-classic,:webThemeDark=tokyo-night,:webTheme=,:webTheme=github-light"; strings.Join(saved, ",") != want {
		t.Errorf("saved %q", saved)
	}
	// A project's theme can't be emptied by the user's scope, and a scope
	// that is neither is refused.
	for _, body := range []map[string]string{{"theme": ""}, {"scope": "team", "theme": "nord"}} {
		if st := post(body); st != http.StatusBadRequest {
			t.Errorf("%v: %d", body, st)
		}
	}
}

// With no config to save to, a theme picked lasts the run.
func TestThemeWithoutConfigLastsTheRun(t *testing.T) {
	s, err := New(Config{Project: &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), Root: "/work/proj"}}, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	c.Jar = jar
	jar.set(ts.URL)
	postJSON(t, c, ts.URL+"/api/theme", map[string]string{"key": "webThemeDark", "theme": "tokyo-night"}, nil)
	var boot struct {
		Theme webtheme.Choice `json:"theme"`
	}
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &boot)
	if want := (webtheme.Choice{Theme: "auto", Light: "xf-light", Dark: "tokyo-night"}); boot.Theme != want {
		t.Errorf("boot theme %+v", boot.Theme)
	}
}

// only is the one project open, for tests: nil for none.
func (s *Server) only() *projectState {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.open {
		return op.p
	}
	return nil
}

// An operation with hasOutput shows when its table was last updated, as a
// table does; one without has no table to look up.
func TestOperationWithOutputShowsUpdated(t *testing.T) {
	op := func(name string, out bool) *project.Action {
		tg := project.Target{Database: "p", Schema: "ops", Name: name}
		return &project.Action{ID: tg.ID(), Target: tg, CanonicalTarget: tg, Kind: project.KindOperation, HasOutput: out, FileName: "definitions/" + name + ".sqlx"}
	}
	g := project.NewGraph([]*project.Action{op("calendar", true), op("cleanup", false)})
	var mu sync.Mutex
	looked := map[string]int{}
	table := func(ctx context.Context, p, d, tb string) (*bigquery.Table, error) {
		mu.Lock()
		looked[tb]++
		mu.Unlock()
		return &bigquery.Table{Type: "TABLE", LastModified: time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)}, nil
	}
	now := func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local) }
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(g), Table: table, Now: now})

	updated := func(id string) (detail, bool) {
		var v actionView
		getJSON(t, c, ts.URL+"/api/action?id="+id, &v)
		for _, a := range v.Pane {
			for _, d := range a.Details {
				if a.ID == project.ActionID(id) && d.Label == "updated" {
					return d, true
				}
			}
		}
		return detail{}, false
	}
	waitFor(t, func() bool { d, _ := updated("p.ops.calendar"); return d.Value != "checking…" && d.Value != "" })
	if d, _ := updated("p.ops.calendar"); d.Value != "2026-10-20 09:00 · 3h ago" || d.Tone != "" {
		t.Errorf("calendar updated %+v", d)
	}
	if d, ok := updated("p.ops.cleanup"); ok {
		t.Errorf("cleanup updated %+v: an operation without hasOutput has no table", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if looked["cleanup"] != 0 {
		t.Errorf("looked up %v: cleanup has no table", looked)
	}
}

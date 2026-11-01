package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/testutil"
)

func loadGraph(t *testing.T, fixture string) *project.Graph {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	data, err = dataform.ExtractCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	g, err := dataform.ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// socketPath is a socket in a short temp dir: the default one on macOS is
// too long for a socket path.
func socketPath(t *testing.T) string {
	t.Helper()
	return testutil.SocketPath(t)
}

// testServer runs a server with an engine over ecfg on path until the test
// ends or stop is called; done is closed when Serve returns.
type testServer struct {
	stop func()
	done chan struct{}
	err  error
}

func startServer(t *testing.T, path, root string, ecfg engine.Config, grace time.Duration) *testServer {
	t.Helper()
	return startBuildServer(t, path, root, ecfg, grace, Build{})
}

// startBuildServer is startServer for a server of build b.
func startBuildServer(t *testing.T, path, root string, ecfg engine.Config, grace time.Duration, b Build) *testServer {
	t.Helper()
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	eng := engine.New(ecfg)
	_ = eng.Start(ctx)
	ts := &testServer{stop: cancel, done: make(chan struct{})}
	go func() {
		defer close(ts.done)
		ts.err = Serve(ctx, ln, ServerConfig{Root: root, Engine: eng, Table: testTable, Grace: grace, Build: b})
	}()
	t.Cleanup(func() { cancel(); <-ts.done })
	return ts
}

func noStart() error { return errors.New("the test should not start a server") }

func connect(t *testing.T, path string, start func() error) *Client {
	t.Helper()
	return connectBuild(t, path, start, Build{})
}

// connectBuild is connect for a client of build b.
func connectBuild(t *testing.T, path string, start func() error, b Build) *Client {
	t.Helper()
	c, err := Connect(context.Background(), ClientConfig{Dial: Dialer(path, start), Parse: dataform.ParseCompileJSON, Build: b})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func next(t *testing.T, ch <-chan engine.State, ok func(engine.State) bool) engine.State {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-ch:
			if ok(s) {
				return s
			}
		case <-timeout:
			t.Fatal("no matching state")
		}
	}
}

func compiled(n int) func(engine.State) bool {
	return func(s engine.State) bool { return s.Compiles == n && !s.Compiling }
}

func fixed(g *project.Graph) func(context.Context) (*project.Graph, error) {
	return func(context.Context) (*project.Graph, error) { return g, nil }
}

func TestClientFollowsState(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	var calls atomic.Int32
	startServer(t, path, "/proj", engine.Config{Compile: func(context.Context) (*project.Graph, error) {
		calls.Add(1)
		return g, nil
	}}, time.Minute)
	c := connect(t, path, noStart)
	states, cancel := c.Subscribe()
	defer cancel()

	st := next(t, states, compiled(1))
	if st.Graph == nil || len(st.Graph.Actions) != len(g.Actions) || st.Graph.DefaultDatabase != "demo-project" {
		t.Fatalf("graph did not come across: %+v", st.Graph)
	}
	first := st.Graph
	select {
	case <-c.FirstCompile():
	default:
		t.Error("FirstCompile not closed")
	}

	c.Recompile("")
	if st := next(t, states, func(s engine.State) bool { return s.Compiling }); st.Graph != first {
		t.Error("a compile starting replaced the graph")
	}
	st = next(t, states, compiled(2))
	if st.Graph != first {
		t.Error("an unchanged graph was sent and parsed again")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("compiles = %d, want 2", n)
	}
}

func TestClientSeesCompileErrors(t *testing.T) {
	path := socketPath(t)
	good, broken := loadGraph(t, "basic.json"), loadGraph(t, "compile_error.json")
	var n atomic.Int32
	startServer(t, path, "/proj", engine.Config{Compile: func(context.Context) (*project.Graph, error) {
		switch n.Add(1) {
		case 1:
			return good, nil
		case 2:
			return broken, nil
		}
		return nil, errors.New("dataform exploded")
	}}, time.Minute)
	c := connect(t, path, noStart)
	states, cancel := c.Subscribe()
	defer cancel()
	g := next(t, states, compiled(1)).Graph
	c.Recompile("")
	if st := next(t, states, compiled(2)); !st.Stale || len(st.Errors) == 0 || st.Graph != g {
		t.Errorf("broken compile: %+v", st)
	}
	c.Recompile("")
	if st := next(t, states, compiled(3)); st.Err == nil || !strings.Contains(st.Err.Error(), "exploded") || st.Errors != nil {
		t.Errorf("failed compile: %+v", st)
	}
}

func TestClientSeesASavedCompile(t *testing.T) {
	path := socketPath(t)
	saved, fresh := loadGraph(t, "basic.json"), loadGraph(t, "incremental.json")
	release := make(chan struct{})
	startServer(t, path, "/proj", engine.Config{
		Compile: func(context.Context) (*project.Graph, error) { <-release; return fresh, nil },
		Saved:   func() *engine.Saved { return &engine.Saved{Graph: saved, Outdated: "project files changed"} },
	}, time.Minute)
	c := connect(t, path, noStart)
	states, cancel := c.Subscribe()
	defer cancel()
	st := next(t, states, func(s engine.State) bool { return s.Compiles == 1 })
	if st.Graph == nil || len(st.Graph.Actions) != len(saved.Actions) || !st.Saved || st.Outdated != "project files changed" || !st.Compiling {
		t.Fatalf("the outdated saved compile should come across while compiling: %+v", st)
	}
	select {
	case <-c.FirstCompile():
	default:
		t.Error("FirstCompile not closed: the saved graph is good enough for jumps")
	}
	close(release)
	if st := next(t, states, compiled(2)); st.Saved || st.Outdated != "" || len(st.Graph.Actions) != len(fresh.Actions) {
		t.Errorf("the startup compile should replace it: %+v", st)
	}
}

func TestDryRunOverTheWire(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	startServer(t, path, "/proj", engine.Config{
		Compile: fixed(g),
		DryRun: func(_ context.Context, project, location, sql string) (*bigquery.DryRun, error) {
			switch sql {
			case "missing":
				return nil, &bigquery.APIError{Status: 404, Message: "Not found: Table x"}
			case "login":
				return nil, errors.Join(bigquery.ErrAuth, errors.New("token expired"))
			case "slow":
				return nil, context.DeadlineExceeded
			}
			return &bigquery.DryRun{Bytes: 42, Accuracy: bigquery.AccuracyPrecise, StatementType: project + "/" + location}, nil
		},
	}, time.Minute)
	c := connect(t, path, noStart)
	ctx := context.Background()

	if r, err := c.DryRun(ctx, "p", "EU", "SELECT 1"); err != nil || r.Bytes != 42 || r.StatementType != "p/EU" {
		t.Errorf("ok: %+v, %v", r, err)
	}
	_, err := c.DryRun(ctx, "p", "EU", "missing")
	if api, ok := errors.AsType[*bigquery.APIError](err); !ok || api.Status != 404 || api.Message != "Not found: Table x" {
		t.Errorf("api error: %v", err)
	}
	if _, err := c.DryRun(ctx, "p", "EU", "login"); !errors.Is(err, bigquery.ErrAuth) {
		t.Errorf("auth error: %v", err)
	}
	if _, err := c.DryRun(ctx, "p", "EU", "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout: %v", err)
	}
}

// testTable is the table lookup of the test servers: only p.d.orders exists.
func testTable(_ context.Context, project, dataset, table string) (*bigquery.Table, error) {
	if project+"."+dataset+"."+table != "p.d.orders" {
		return nil, &bigquery.APIError{Status: 404, Message: "Not found: Table " + table}
	}
	return &bigquery.Table{Type: "TABLE", LastModified: time.UnixMilli(1759835220000).UTC()}, nil
}

func TestTableOverTheWire(t *testing.T) {
	path := socketPath(t)
	startServer(t, path, "/proj", engine.Config{Compile: fixed(loadGraph(t, "basic.json"))}, time.Minute)
	c := connect(t, path, noStart)
	ctx := context.Background()

	if tbl, err := c.Table(ctx, "p", "d", "orders"); err != nil || tbl.Type != "TABLE" || !tbl.LastModified.Equal(time.UnixMilli(1759835220000)) {
		t.Errorf("ok: %+v, %v", tbl, err)
	}
	_, err := c.Table(ctx, "p", "d", "nope")
	if api, ok := errors.AsType[*bigquery.APIError](err); !ok || api.Status != 404 {
		t.Errorf("a table that doesn't exist: %v", err)
	}
}

func TestDryRunOff(t *testing.T) {
	path := socketPath(t)
	startServer(t, path, "/proj", engine.Config{Compile: fixed(loadGraph(t, "basic.json"))}, time.Minute)
	c := connect(t, path, noStart)
	if _, err := c.DryRun(context.Background(), "p", "EU", "SELECT 1"); !errors.Is(err, engine.ErrNoDryRun) {
		t.Errorf("got %v", err)
	}
}

func TestActiveFileReachesEveryClient(t *testing.T) {
	path := socketPath(t)
	root := t.TempDir()
	file := filepath.Join(root, "definitions", "a.sqlx")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("select 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	startServer(t, path, root, engine.Config{Compile: fixed(loadGraph(t, "basic.json"))}, time.Minute)
	editor, tui := connect(t, path, noStart), connect(t, path, noStart)
	// Both are connected once they have a state.
	for _, c := range []*Client{editor, tui} {
		ch, cancel := c.Subscribe()
		next(t, ch, compiled(1))
		cancel()
	}
	editor.ActiveFile("BufEnter", filepath.Join(root, "outside", "..", "..", "elsewhere.sqlx")) // dropped
	editor.ActiveFile("CursorHold", file)
	select {
	case ev := <-tui.Editor():
		if ev != (project.EditorEvent{Event: "CursorHold", File: "definitions/a.sqlx"}) {
			t.Errorf("got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no editor event")
	}
}

// TestTakeover stops the server under a client, which starts another and
// carries on: the graph stays on show, compiles keep counting up and dry
// runs go to the new server.
func TestTakeover(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	ecfg := engine.Config{
		Compile: fixed(g),
		DryRun: func(context.Context, string, string, string) (*bigquery.DryRun, error) {
			return &bigquery.DryRun{Bytes: 7}, nil
		},
	}
	first := startServer(t, path, "/proj", ecfg, time.Minute)
	var mu sync.Mutex
	var second *testServer
	start := func() error {
		mu.Lock()
		defer mu.Unlock()
		if second != nil {
			return errors.New("started twice")
		}
		second = startServer(t, path, "/proj", ecfg, time.Minute)
		return nil
	}
	c := connect(t, path, start)
	states, cancel := c.Subscribe()
	defer cancel()
	shown := next(t, states, compiled(1)).Graph

	first.stop()
	<-first.done
	// Until the new server has compiled, the old graph stays on show.
	st := next(t, states, func(s engine.State) bool {
		if s.Graph == nil {
			t.Error("the graph went during the takeover")
		}
		return s.Compiles == 2
	})
	if st.Graph == shown {
		t.Error("no graph from the new server")
	}
	mu.Lock()
	started := second != nil
	mu.Unlock()
	if !started {
		t.Fatal("no new server was started")
	}
	if r, err := c.DryRun(context.Background(), "p", "EU", "SELECT 1"); err != nil || r.Bytes != 7 {
		t.Errorf("dry run after takeover: %+v, %v", r, err)
	}
}

// TestNewerBuildReplacesServer connects a client of a newer xf build to a
// server of an older one: the server stops, the client starts one of its
// own build, and a client of the older build carries on with that one
// rather than replacing it in turn.
func TestNewerBuildReplacesServer(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	ecfg := engine.Config{Compile: fixed(g)}
	older := Build{Time: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), Size: 20 << 20}
	newer := Build{Time: older.Time.Add(time.Minute), Size: older.Size}

	first := startBuildServer(t, path, "/proj", ecfg, time.Minute, older)
	oldClient := connectBuild(t, path, noStart, older)
	oldStates, cancelOld := oldClient.Subscribe()
	defer cancelOld()
	next(t, oldStates, compiled(1))
	select {
	case <-first.done:
		t.Fatal("a client of the same build stopped the server")
	case <-time.After(100 * time.Millisecond):
	}

	var mu sync.Mutex
	var second *testServer
	start := func() error {
		mu.Lock()
		defer mu.Unlock()
		if second != nil {
			return errors.New("started twice")
		}
		second = startBuildServer(t, path, "/proj", ecfg, time.Minute, newer)
		return nil
	}
	newClient := connectBuild(t, path, start, newer)
	select {
	case <-first.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the older server didn't stop")
	}
	if first.err != nil {
		t.Errorf("the older server stopped with %v", first.err)
	}
	newStates, cancelNew := newClient.Subscribe()
	defer cancelNew()
	// The new client's first state may be the old server's; its own
	// server's compile follows.
	next(t, newStates, func(s engine.State) bool {
		mu.Lock()
		defer mu.Unlock()
		return s.Compiles >= 1 && !s.Compiling && second != nil
	})
	// The older client moves to the newer server, which keeps serving it.
	next(t, oldStates, compiled(2))
	mu.Lock()
	srv := second
	mu.Unlock()
	select {
	case <-srv.done:
		t.Fatal("a client of an older build stopped the newer server")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBuildNewer(t *testing.T) {
	t0 := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		b, old Build
		want   bool
	}{
		{Build{Time: t0.Add(time.Second), Size: 1}, Build{Time: t0, Size: 2}, true},
		{Build{Time: t0, Size: 2}, Build{Time: t0.Add(time.Second), Size: 1}, false},
		{Build{Time: t0, Size: 2}, Build{Time: t0, Size: 1}, true},
		{Build{Time: t0, Size: 1}, Build{Time: t0, Size: 1}, false},
		{Build{Time: t0, Size: 1}, Build{}, false},
		{Build{}, Build{Time: t0, Size: 1}, false},
	} {
		if got := tc.b.newer(tc.old); got != tc.want {
			t.Errorf("%v newer than %v = %v, want %v", tc.b, tc.old, got, tc.want)
		}
	}
}

// TestDryRunInFlightMovesToNewServer loses the server while a dry run waits
// on it; the client asks the next server instead.
func TestDryRunInFlightMovesToNewServer(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	asked := make(chan struct{}, 1)
	stuck := engine.Config{Compile: fixed(g), DryRun: func(ctx context.Context, _, _, _ string) (*bigquery.DryRun, error) {
		asked <- struct{}{}
		select {} // never answers
	}}
	first := startServer(t, path, "/proj", stuck, time.Minute)
	start := func() error {
		startServer(t, path, "/proj", engine.Config{Compile: fixed(g), DryRun: func(context.Context, string, string, string) (*bigquery.DryRun, error) {
			return &bigquery.DryRun{Bytes: 9}, nil
		}}, time.Minute)
		return nil
	}
	c := connect(t, path, start)
	res := make(chan error, 1)
	go func() {
		r, err := c.DryRun(context.Background(), "p", "EU", "SELECT 1")
		if err == nil && r.Bytes != 9 {
			err = errors.New("answered by the wrong server")
		}
		res <- err
	}()
	<-asked
	first.stop()
	select {
	case err := <-res:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dry run never finished")
	}
}

func TestServerStopsWhenIdle(t *testing.T) {
	path := socketPath(t)
	ts := startServer(t, path, "/proj", engine.Config{Compile: fixed(loadGraph(t, "basic.json"))}, 100*time.Millisecond)
	c := connect(t, path, noStart)
	time.Sleep(300 * time.Millisecond) // longer than the grace: a client keeps it up
	select {
	case <-ts.done:
		t.Fatal("stopped with a client connected")
	default:
	}
	c.Close()
	select {
	case <-ts.done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop after its last client left")
	}
	if ts.err != nil {
		t.Errorf("Serve: %v", ts.err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket left behind: %v", err)
	}
}

func TestListen(t *testing.T) {
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path); !errors.Is(err, ErrRunning) {
		t.Errorf("second listener: got %v, want ErrRunning", err)
	}
	_ = ln.Close()
	ln, err = Listen(path)
	if err != nil {
		t.Fatalf("listening again after the server stopped: %v", err)
	}
	_ = ln.Close()
}

func TestSocketPath(t *testing.T) {
	root := t.TempDir()
	a := SocketPath(root, "dataform")
	if a != SocketPath(root, "dataform") {
		t.Fatal("not deterministic")
	}
	checkSocketShape(t, a)
	if SocketPath(t.TempDir(), "dataform") == a {
		t.Error("two projects share a socket")
	}
	if SocketPath(root, "dbt") == a {
		t.Error("two backends of one directory share a socket")
	}
	link := filepath.Join(t.TempDir(), "link")
	testutil.Symlink(t, root, link)
	if SocketPath(link, "dataform") != a {
		t.Error("a symlink to the project maps to another socket")
	}
}

// A staged graph reaches the client as one, and the client's Focus reaches
// the engine.
func TestStagedStateAndFocusOverTheWire(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	var id project.ActionID
	for k := range g.Actions {
		id = k
		break
	}
	g.Actions[id].SQL = project.SQLRaw
	focused := make(chan project.ActionID, 1)
	release := make(chan struct{})
	startServer(t, path, "/proj", engine.Config{
		Stage: func(context.Context, *project.Graph) (*project.Graph, bool, error) { return g, false, nil },
		Compile: func(ctx context.Context) (*project.Graph, error) {
			select {
			case <-release:
				return g, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		Focus: func(ctx context.Context, _ *project.Graph, id project.ActionID) (*project.Graph, error) {
			focused <- id
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}, time.Minute)
	c := connect(t, path, noStart)
	states, cancel := c.Subscribe()
	defer cancel()

	if st := next(t, states, func(s engine.State) bool { return s.Graph != nil }); !st.Partial || st.Compiles != 0 || !st.Compiling {
		t.Fatalf("the staged graph: %+v", st)
	}
	select {
	case <-c.FirstCompile():
		t.Error("FirstCompile closed on a staged graph")
	default:
	}
	c.Focus(id)
	select {
	case got := <-focused:
		if got != id {
			t.Errorf("the engine was asked for %s, want %s", got, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Focus never reached the engine")
	}
	if st := next(t, states, func(s engine.State) bool { return s.Focus != "" }); st.Focus != id {
		t.Errorf("focus = %s", st.Focus)
	}
	close(release)
	if st := next(t, states, compiled(1)); st.Partial || st.Focus != "" {
		t.Errorf("after the compile: %+v", st)
	}
}

// A theme one client sets reaches every client with the engine's state.
func TestThemeReachesEveryClient(t *testing.T) {
	path := socketPath(t)
	g := loadGraph(t, "basic.json")
	startServer(t, path, "/proj", engine.Config{Compile: func(context.Context) (*project.Graph, error) { return g, nil }}, time.Minute)
	a, b := connect(t, path, noStart), connect(t, path, noStart)
	sa, cancelA := a.Subscribe()
	defer cancelA()
	sb, cancelB := b.Subscribe()
	defer cancelB()
	next(t, sa, compiled(1))
	next(t, sb, compiled(1))

	a.SetTheme("dark")
	if st := next(t, sb, func(s engine.State) bool { return s.Theme != "" }); st.Theme != "dark" {
		t.Errorf("theme %q", st.Theme)
	}
	if st := next(t, sa, func(s engine.State) bool { return s.Theme != "" }); st.Theme != "dark" {
		t.Errorf("the setter's theme %q", st.Theme)
	}
}

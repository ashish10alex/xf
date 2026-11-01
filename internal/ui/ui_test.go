package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

// Animation frames would otherwise tick for as long as a test compile is
// pending; frames are driven by hand in TestCompileAnimation.
func init() {
	animate = func() tea.Cmd { return nil }
	jobsTick = func() tea.Cmd { return nil }
}

// fakeCompiler returns fixtures in order; the last one repeats.
type fakeCompiler struct {
	mu       sync.Mutex
	t        *testing.T
	fixtures []string
	calls    int
}

func (f *fakeCompiler) compile(ctx context.Context) (*project.Graph, error) {
	f.mu.Lock()
	name := f.fixtures[min(f.calls, len(f.fixtures)-1)]
	f.calls++
	f.mu.Unlock()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		f.t.Fatal(err)
	}
	data, err = dataform.ExtractCompileJSON(data)
	if err != nil {
		f.t.Fatal(err)
	}
	return dataform.ParseCompileJSON(data)
}

// testNow is the fake clock of the tests' engines and models.
func testNow() time.Time { return time.Date(2026, 9, 29, 21, 7, 6, 0, time.UTC) }

// fakeEngine compiles synchronously when asked, queueing the states a real
// engine would send; drive delivers them. Its first compile runs when the
// model subscribes, as the real engine's starts with the program.
type fakeEngine struct {
	compile func(context.Context) (*project.Graph, error)
	mu      sync.Mutex
	state   engine.State
	pending []engine.State
	// hold keeps queued states back, as if the program hadn't started
	// reading them yet.
	hold bool
	// focused are the actions Focus was asked for, in order.
	focused []project.ActionID
}

func newFakeEngine(compile func(context.Context) (*project.Graph, error)) *fakeEngine {
	return &fakeEngine{compile: compile, state: engine.State{}.Begin("", testNow())}
}

// Subscribe returns no channel: drive pulls states with drain instead.
func (f *fakeEngine) Subscribe() (<-chan engine.State, func()) {
	f.mu.Lock()
	f.pending = append(f.pending, f.state)
	f.mu.Unlock()
	f.finish()
	return nil, func() {}
}

func (f *fakeEngine) Focus(id project.ActionID) { f.focused = append(f.focused, id) }

// SetTheme queues the state a real engine would send.
func (f *fakeEngine) SetTheme(theme string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Theme = theme
	f.pending = append(f.pending, f.state)
}

func (f *fakeEngine) Recompile(reason string) {
	f.begin(reason)
	f.finish()
}

// begin queues the state of a compile that has started but not finished.
func (f *fakeEngine) begin(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = f.state.Begin(reason, testNow())
	f.pending = append(f.pending, f.state)
}

func (f *fakeEngine) finish() {
	g, err := f.compile(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = f.state.Finish(g, err, time.Second, testNow())
	f.pending = append(f.pending, f.state)
}

func (f *fakeEngine) drain() []tea.Msg {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hold {
		return nil
	}
	var out []tea.Msg
	for _, st := range f.pending {
		out = append(out, engineMsg{st})
	}
	f.pending = nil
	return out
}

// realEngine runs the real engine over compile, for tests that run the
// program's own event loop.
func realEngine(t *testing.T, compile func(context.Context) (*project.Graph, error), watch <-chan struct{}) *engine.Engine {
	t.Helper()
	cfg := engine.Config{Compile: compile, Now: testNow}
	if watch != nil {
		cfg.Watch = func(context.Context) (<-chan struct{}, error) { return watch, nil }
	}
	e := engine.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *fakeCompiler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeRunner records run requests and returns runs whose output is already
// complete, so drive() can process them synchronously.
type fakeRunner struct {
	mu    sync.Mutex
	opts  []project.RunOptions
	lines []string
	res   project.RunResult
}

func (f *fakeRunner) start(_ context.Context, opts project.RunOptions) (*project.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opts = append(f.opts, opts)
	lines := make(chan string, len(f.lines))
	for _, l := range f.lines {
		lines <- l
	}
	close(lines)
	done := make(chan project.RunResult, 1)
	done <- f.res
	return &project.Run{Cmdline: "dataform " + strings.Join(dataform.RunArgs(opts), " "), Lines: lines, Done: done}, nil
}

func (f *fakeRunner) calls() []project.RunOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]project.RunOptions(nil), f.opts...)
}

func newTestModel(t *testing.T, fixtures ...string) (Model, *fakeCompiler) {
	t.Helper()
	m, fc, _ := newTestModelWithRunner(t, &fakeRunner{}, fixtures...)
	return m, fc
}

func newTestModelWithRunner(t *testing.T, fr *fakeRunner, fixtures ...string) (Model, *fakeCompiler, *fakeRunner) {
	t.Helper()
	fc := &fakeCompiler{t: t, fixtures: fixtures}
	m := New(Deps{
		Root:     "/work/demo",
		Engine:   newFakeEngine(fc.compile),
		StartRun: fr.start,
		Now:      testNow,
	})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	return closePalette(t, m), fc, fr
}

// drive feeds messages through Update and runs the resulting commands
// synchronously. Commands that don't return promptly (cursor blink ticks) are
// dropped, so the test never waits on timers.
func drive(t *testing.T, m Model, first tea.Cmd, msgs ...tea.Msg) Model {
	t.Helper()
	var queue []tea.Msg
	run := func(cmd tea.Cmd) {
		queue = append(queue, runCmd(cmd)...)
	}
	// pump delivers what the fake engine has queued, as its subscription
	// channel would.
	pump := func() bool {
		fe, ok := m.deps.Engine.(*fakeEngine)
		if !ok {
			return false
		}
		states := fe.drain()
		queue = append(queue, states...)
		return len(states) > 0
	}
	run(first)
	pump()
	queue = append(queue, msgs...)
	for i := 0; len(queue) > 0 || pump(); i++ {
		if i > 200 {
			t.Fatal("message loop did not settle")
		}
		msg := queue[0]
		queue = queue[1:]
		next, cmd := m.Update(msg)
		m = next.(Model)
		run(cmd)
		pump()
	}
	return m
}

func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil || isCursorBlink(cmd) {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		switch msg := msg.(type) {
		case nil:
			return nil
		case tea.BatchMsg:
			var out []tea.Msg
			for _, c := range msg {
				out = append(out, runCmd(c)...)
			}
			return out
		case tea.QuitMsg:
			return nil
		default:
			return []tea.Msg{msg}
		}
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

// isCursorBlink reports whether cmd is a text input's cursor blink. Every
// keystroke into an input returns one, and it sleeps out the blink interval,
// so runCmd drops it unrun rather than waiting for its timeout on each key.
func isCursorBlink(cmd tea.Cmd) bool {
	fn := runtime.FuncForPC(reflect.ValueOf(cmd).Pointer())
	return fn != nil && strings.HasPrefix(fn.Name(), "charm.land/bubbles/v2/cursor.")
}

func press(keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		out = append(out, keyMsg(k))
	}
	return out
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c", "ctrl+n", "ctrl+p", "ctrl+d", "ctrl+u":
		return tea.KeyPressMsg{Code: rune(k[len(k)-1]), Mod: tea.ModCtrl}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func typeText(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

func screen(m Model) string { return ansi.Strip(m.render()) }

// paletteView is the palette alone, so assertions about the list aren't
// confused by the same names appearing in the SQL.
func paletteView(m Model) string {
	return ansi.Strip(m.palette.view(m.styles, m.palette.box(m.width, m.geometry().bodyH)))
}

// closePalette closes the palette the first compile opens, so tests start
// from the plain panes.
func closePalette(t *testing.T, m Model) Model {
	t.Helper()
	if m.palette.open {
		m = drive(t, m, nil, press("esc")...)
	}
	return m
}

// find picks an action through the palette.
func find(t *testing.T, m Model, query string) Model {
	t.Helper()
	msgs := append(press("/"), typeText(query)...)
	return drive(t, m, nil, append(msgs, press("enter")...)...)
}

func assertContains(t *testing.T, view string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(view, w) {
			t.Errorf("view does not contain %q:\n%s", w, view)
		}
	}
}

func assertNotContains(t *testing.T, view string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(view, w) {
			t.Errorf("view unexpectedly contains %q:\n%s", w, view)
		}
	}
}

// startOn selects the action, as jumping to it from Lineage would.
func startOn(t *testing.T, m Model, id project.ActionID) Model {
	t.Helper()
	a := m.graph.Actions[id]
	if a == nil {
		t.Fatalf("no action %s", id)
	}
	m.palette.close()
	m.selectAction(a)
	return m
}

func TestStartupOpensPalette(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	if !m.palette.open {
		t.Fatal("palette not opened after the first compile")
	}
	assertContains(t, screen(m),
		"find action", "10/10", "shop.customers", "analytics.daily_orders", "analytics.weekly_revenue",
		"DATAFORM", "demo · 10 actions · core 3.0.43 · compiled 21:07:06", "idle",
	)
	// Labels are targets, not the files that define them.
	assertNotContains(t, paletteView(m), "definitions", ".sqlx", "customers_source")

	// esc leaves nothing selected, and the card says how to find an action.
	m = drive(t, m, nil, press("esc")...)
	assertContains(t, screen(m), "press / to find an action", "▸ 2 lineage", "0 sql")
	assertNotContains(t, screen(m), "╭─find action")
	if m.selected != nil {
		t.Errorf("selected = %v", m.selected.ID)
	}
}

func TestPalettePicksAction(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("3")...) // focus Run, to see enter move it to SQL
	m = find(t, m, "weekly")
	if m.palette.open {
		t.Fatal("enter did not close the palette")
	}
	if m.selected == nil || m.selected.ID != "demo-project.analytics.weekly_revenue" {
		t.Fatalf("selected = %v", m.selected)
	}
	if m.focus != paneSQL {
		t.Errorf("focus = %v after picking, want the SQL", m.focus)
	}
	view := screen(m)
	assertContains(t, view, "0 sql · analytics.weekly_revenue · view", "DATE_TRUNC",
		"▸ VIEW  demo-project.analytics.weekly_revenue", "1 actions · definitions/weekly_revenue.sqlx")

	// Matching is fuzzy: "dord" finds daily_orders, the table ahead of its
	// assertions (a shorter label scores higher).
	m = find(t, m, "dord")
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("selected = %v", m.selected.ID)
	}
	assertContains(t, screen(m), "▸ TABLE      demo-project.analytics.daily_orders")
}

func TestPaletteMoveKeys(t *testing.T) {
	// The arrows and ctrl+n/ctrl+p move; j and k are typed into the query.
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, append(press("/"), typeText("stg")...)...)
	m = drive(t, m, nil, press("ctrl+n")...)
	if m.palette.cursor != 1 || m.palette.input.Value() != "stg" {
		t.Fatalf("after ctrl+n: cursor %d, query %q", m.palette.cursor, m.palette.input.Value())
	}
	m = drive(t, m, nil, press("ctrl+p")...)
	if m.palette.cursor != 0 {
		t.Fatalf("after ctrl+p: cursor %d", m.palette.cursor)
	}
	m = drive(t, m, nil, typeText("jk")...)
	if m.palette.input.Value() != "stgjk" {
		t.Errorf("query = %q, want j and k typed", m.palette.input.Value())
	}
}

func TestPaletteListsEachActionOfAFile(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, append(press("/"), typeText("daily_orders")...)...)
	view := paletteView(m)
	// A file with several actions gets a row for each; a label too long for
	// the row loses its dataset.
	assertContains(t, view, "3/10", "analytics.daily_orders", "table", "│   analytics_daily_orders_assertions_uniqueKey_0", "_rowConditions", "assertion")
	assertNotContains(t, view, "…", "analytics.analytics_daily_orders_assertions")
	for _, l := range strings.Split(view, "\n")[2:5] { // the result rows
		if !strings.HasSuffix(l, "  │") && !strings.HasSuffix(l, "table │") && !strings.HasSuffix(l, "assertion │") {
			t.Errorf("kind not followed by a margin before the border: %q", l)
		}
	}
	m = drive(t, m, nil, press("down", "down", "enter")...)
	if m.selected == nil || m.selected.Kind != project.KindAssertion {
		t.Errorf("selected = %v, want one of the file's assertions", m.selected)
	}
}

func TestPaletteFiltersAndEscCancels(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.weekly_revenue")
	m = drive(t, m, nil, append(press("/"), typeText("orders")...)...)
	view := paletteView(m)
	// daily_orders and its two assertions, stg_orders and shop.orders.
	assertContains(t, view, "5/10", "stg_orders", "daily_orders", "shop.orders")
	assertNotContains(t, view, "stg_customers", "weekly_revenue")

	m = drive(t, m, nil, press("esc")...)
	if m.palette.open {
		t.Fatal("esc did not close the palette")
	}
	if m.selected.ID != "demo-project.analytics.weekly_revenue" {
		t.Errorf("esc changed the selection to %v", m.selected.ID)
	}
	// The query starts empty each time.
	m = drive(t, m, nil, press("/")...)
	if m.palette.input.Value() != "" || len(m.palette.list) != 10 {
		t.Errorf("reopened with query %q and %d rows", m.palette.input.Value(), len(m.palette.list))
	}
	m = drive(t, m, nil, typeText("zzz")...)
	assertContains(t, paletteView(m), "no matches", "0/10")
}

func TestPaletteSuppressesGlobalKeys(t *testing.T) {
	m, fc := newTestModel(t, "basic.json")
	m = drive(t, m, nil, append(press("/"), typeText("cq")...)...)
	if fc.callCount() != 1 {
		t.Errorf("typing c in the palette recompiled (%d compiles)", fc.callCount())
	}
	if m.palette.input.Value() != "cq" {
		t.Errorf("query = %q", m.palette.input.Value())
	}
}

// TestPaletteMatchesWordsInAnyOrder: each space-separated word must match the
// name or file path on its own, items holding every word as written outrank
// scattered matches, and a table comes before the assertions of its file.
func TestPaletteMatchesWordsInAnyOrder(t *testing.T) {
	g := &project.Graph{Actions: map[project.ActionID]*project.Action{}}
	for _, a := range []struct{ name, file string }{
		{"0500_STOCK_HOLDS", "definitions/t1-stock_plan/output/0500_STOCK_HOLDS.sqlx"},
		{"0500_POS_ORDERS_IN_HOLD", "definitions/t2-pos-holds/0500_POS_ORDERS_IN_HOLD.sqlx"},
		{"t2_0500_POS_ORDERS_IN_HOLD_assertions_uniqueKey_0", "definitions/t2-pos-holds/0500_POS_ORDERS_IN_HOLD.sqlx"},
		{"0400_POS_ORDERS", "definitions/t2-pos/0400_POS_ORDERS.sqlx"},
		{"0500_polo_holders_rowConditions", "definitions/t1/polo.sqlx"},
	} {
		target := project.Target{Schema: "s", Name: a.name}
		g.Actions[target.ID()] = &project.Action{ID: target.ID(), Target: target, FileName: a.file}
	}
	p := newPalette("dataform")
	p.show(paletteItems(g), nil, false)
	p.input.SetValue("0500 pos  holds")
	p.filter()
	var got []string
	for _, m := range p.list {
		got = append(got, m.item.Label)
	}
	// 0500_STOCK_HOLDS matches "pos" scattered through plan/output, as in VS
	// Code, so it follows the items that hold every word as written.
	want := []string{"s.0500_POS_ORDERS_IN_HOLD", "s.t2_0500_POS_ORDERS_IN_HOLD_assertions_uniqueKey_0", "s.0500_STOCK_HOLDS", "s.0500_polo_holders_rowConditions"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("matches = %v, want %v", got, want)
	}
}

func TestPaletteRecentFirst(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = find(t, m, "weekly")
	m = find(t, m, "stg_cust")
	m = find(t, m, "refresh")
	// Current action first, then the most recent; the cursor starts on the
	// previous action so / enter switches back.
	m = drive(t, m, nil, press("/")...)
	var got []project.ActionID
	for _, it := range m.palette.list[:3] {
		got = append(got, it.item.Action.ID)
	}
	want := []project.ActionID{"demo-project.analytics.refresh_permissions", "demo-project.staging.stg_customers", "demo-project.analytics.weekly_revenue"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("first rows = %v, want %v", got, want)
	}
	m = drive(t, m, nil, press("enter")...)
	if m.selected.ID != "demo-project.staging.stg_customers" {
		t.Errorf("/ enter selected %v, want the previous action", m.selected.ID)
	}
}

func TestIncrementalToggle(t *testing.T) {
	m, _ := newTestModel(t, "incremental.json")
	m = startOn(t, m, "demo-project.reporting.daily_events")
	view := screen(m)
	assertContains(t, view, "reporting.daily_events · incremental [incremental]",
		"-- incremental pre_operations", "MAX(event_date)", "-- incremental query", "-- incremental post_operations")
	m = drive(t, m, nil, press("i")...) // v is visual mode in the focused SQL
	view = screen(m)
	assertContains(t, view, "[full]", "-- pre_operations", "DATE '2000-01-01'", "-- query", "-- post_operations")
	assertNotContains(t, view, "MAX(event_date)")
}

func TestCompileErrorsKeepLastGoodGraph(t *testing.T) {
	m, _ := newTestModel(t, "basic.json", "compile_error.json")
	m = startOn(t, m, "demo-project.analytics.weekly_revenue")
	m = drive(t, m, nil, press("c")...)
	view := screen(m)
	assertContains(t, view,
		"compile errors", "definitions/bad_js.js", "deliberate error in bad_js.js", "Missing dependency detected",
		"stale · ✗ 4 compile errors (e)",
	)
	if m.graph == nil || m.graph.Actions["demo-project.analytics.weekly_revenue"] == nil {
		t.Fatal("last good graph was replaced")
	}
	m = drive(t, m, nil, press("esc")...)
	view = screen(m)
	assertNotContains(t, view, "deliberate error")
	assertContains(t, view, "0 sql · analytics.weekly_revenue", "stale")
	m = drive(t, m, nil, press("e")...)
	assertContains(t, screen(m), "deliberate error")
}

func TestErrorsOnFirstCompileShowPartialGraph(t *testing.T) {
	m, _ := newTestModel(t, "compile_error.json")
	view := screen(m)
	assertContains(t, view, "compile errors", "✗ 4 compile errors (e)")
	assertNotContains(t, view, "stale")
	if m.palette.open {
		t.Error("palette opened over the compile errors")
	}
	m = drive(t, m, nil, press("esc", "/")...)
	assertContains(t, paletteView(m), "good", "missing_ref")
}

func TestCompileErrorsWrapScrollAndZoom(t *testing.T) {
	msg := "Error: Could not find a recent installed version of @dataform/core in the project. " +
		"Check that either `dataform install` has been run, or that workflow_settings.yaml sets dataformCoreVersion."
	for i := range 40 {
		msg += fmt.Sprintf("\n    at frame%02d (/Users/someone/.nvm/versions/node/v24.14.0/lib/node_modules/@dataform/cli/worker_bundle.js:1:%d)", i, i)
	}
	compiles := 0
	m := New(Deps{
		Root: "/work/demo",
		Engine: newFakeEngine(func(context.Context) (*project.Graph, error) {
			compiles++
			return nil, errors.New(msg)
		}),
		Now: testNow,
	})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	view := screen(m)
	// Wrapped, not cut off at the box's edge.
	assertContains(t, view, "compile errors", "dataformCoreVersion.", "frame00", "↓", "j/k scroll")
	assertNotContains(t, view, "frame39")

	m = drive(t, m, nil, press("G")...)
	assertContains(t, screen(m), "frame39", "↑")

	m = drive(t, m, nil, press("z")...)
	view = screen(m)
	assertContains(t, view, "errors · zoom (esc)", "z unzoom")
	m = drive(t, m, nil, press("esc")...)
	if !m.showErrors || m.errorsView.zoomed {
		t.Fatalf("esc in the zoomed errors: showErrors=%v zoomed=%v, want the box back at its normal size", m.showErrors, m.errorsView.zoomed)
	}

	before := compiles
	m = drive(t, m, nil, press("c")...)
	if compiles != before+1 {
		t.Errorf("c in the errors box ran %d compiles, want 1", compiles-before)
	}
	m = drive(t, m, nil, press("esc")...)
	if m.showErrors {
		t.Error("esc didn't close the errors box")
	}
}

func TestCleanRecompileClearsErrorsAndKeepsSelection(t *testing.T) {
	m, _ := newTestModel(t, "basic.json", "compile_error.json", "basic.json")
	m = startOn(t, m, "demo-project.analytics.weekly_revenue")
	m = drive(t, m, nil, press("c", "esc", "c")...)
	view := screen(m)
	assertContains(t, view, "idle", "0 sql · analytics.weekly_revenue")
	assertNotContains(t, view, "compile error", "stale")
}

func TestSelectionClearedWhenActionDisappears(t *testing.T) {
	m, _ := newTestModel(t, "basic.json", "incremental.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, press("c")...)
	if m.selected != nil {
		t.Fatalf("selected = %v after its action disappeared", m.selected.ID)
	}
	if m.palette.open {
		t.Error("palette opened after a recompile; only the first compile opens it")
	}
	assertContains(t, screen(m), "press / to find an action")
}

func TestTabCyclesFocus(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	if m.focus != paneSQL {
		t.Fatalf("initial focus = %v", m.focus)
	}
	// Top to bottom: actions, lineage (which expands), run, sql.
	m = drive(t, m, nil, press("tab")...)
	if m.focus != paneActions {
		t.Fatalf("focus after tab = %v", m.focus)
	}
	m = drive(t, m, nil, press("tab")...)
	if m.focus != paneLineage || !m.lineageOpen {
		t.Fatalf("focus after two tabs = %v, lineage open = %v", m.focus, m.lineageOpen)
	}
	m = drive(t, m, nil, press("tab", "tab")...)
	if m.focus != paneSQL {
		t.Fatalf("focus after four tabs = %v", m.focus)
	}
	// j scrolls the SQL rather than changing the selection.
	before := m.selected
	m = drive(t, m, nil, press("j")...)
	if m.selected != before {
		t.Error("j in the SQL pane changed the selection")
	}
	m = drive(t, m, nil, press("shift+tab", "2")...)
	if m.focus != paneLineage {
		t.Fatalf("focus = %v", m.focus)
	}
}

func TestTooSmallTerminal(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 40, Height: 9})
	assertContains(t, screen(m), "terminal too small")
}

// A phone in landscape is about 19 rows: the panes stack, the log open
// too, and shorter than that the focused pane shows alone.
func TestShortTerminal(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 103, Height: 19})
	m = drive(t, m, nil, press("L", "2")...)
	g := m.geometry()
	if g.solo || g.logH < 3 || g.sqlH < 3 {
		t.Fatalf("19 rows with the log: %+v", g)
	}
	if got := strings.Count(screen(m), "\n") + 1; got != 19 {
		t.Errorf("screen is %d rows, want 19", got)
	}
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 103, Height: 12})
	if !m.geometry().solo {
		t.Fatalf("12 rows with the log: %+v", m.geometry())
	}
	v := screen(m)
	assertContains(t, v, "2 lineage · tab: next pane")
	if strings.Contains(v, "0 sql") {
		t.Error("solo layout shows a pane besides the focused one")
	}
	if got := strings.Count(v, "\n") + 1; got != 12 {
		t.Errorf("screen is %d rows, want 12", got)
	}
}

// lineageView is the expanded Lineage pane's content at 60 columns.
func lineageView(m Model) string {
	m.lineageOpen = true
	m.layout()
	return ansi.Strip(m.lineage.view(m.styles, 60, true))
}

func TestLineageShowsDirectDepsAndDependents(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	view := lineageView(m)
	assertContains(t, view,
		"upstream (2)", "├ staging.stg_customers", "└ staging.stg_orders",
		"downstream (4)", "analytics.refresh_permissions", "operation", "analytics.weekly_revenue", "view",
	)
	assertNotContains(t, view, "shop.orders") // indirect: only direct lineage
	assertContains(t, screen(m), "▸ 2 lineage · ↑2 ↓4")
}

func TestLineageKindLabelWidth(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	// Wide panes spell the kind out; narrow ones fall back to the abbreviation.
	assertContains(t, lineageView(m), "operation", "assertion")
	m.lineageOpen = true
	m.layout()
	narrow := ansi.Strip(m.lineage.view(m.styles, 30, true))
	assertContains(t, narrow, "op", "asrt")
	assertNotContains(t, narrow, "operation")
	for _, l := range strings.Split(lineageView(m), "\n") {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("line %q is %d cells, wider than the pane", l, w)
		}
	}
}

func TestLineageNoneAndDeclaration(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "raw-project.shop.customers")
	view := lineageView(m)
	assertContains(t, view, "upstream (0)", "none", "downstream (1)", "└ staging.stg_customers")
}

func TestLineageJumpAndBack(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, press("2")...)
	// Upstream entries come first; move to the second (stg_orders) and jump.
	m = drive(t, m, nil, press("j", "enter")...)
	if m.selected == nil || m.selected.ID != "demo-project.staging.stg_orders" {
		t.Fatalf("after jump selected = %v", m.selected)
	}
	assertContains(t, screen(m), "0 sql · staging.stg_orders", "upstream (1)", "└ shop.orders")

	// Jump to a declaration.
	m = drive(t, m, nil, press("enter")...)
	if m.selected.ID != "raw-project.shop.orders" {
		t.Fatalf("selected = %v", m.selected.ID)
	}

	m = drive(t, m, nil, press("backspace")...)
	if m.selected.ID != "demo-project.staging.stg_orders" {
		t.Fatalf("after back selected = %v", m.selected.ID)
	}
	m = drive(t, m, nil, press("backspace")...)
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Fatalf("after second back selected = %v", m.selected.ID)
	}
	m = drive(t, m, nil, press("backspace")...)
	assertContains(t, screen(m), "no earlier action to go back to")
}

func TestPalettePickGoesBackWithBackspace(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = find(t, m, "weekly")
	m = drive(t, m, nil, press("2", "backspace")...)
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("backspace after a palette pick selected %v", m.selected.ID)
	}
}

func TestRecompileShowsNewResult(t *testing.T) {
	m, fc := newTestModel(t, "basic.json", "incremental.json")
	m.deps.Engine.Recompile(engine.ReasonFileChanged)
	m = drive(t, m, nil)
	if fc.callCount() != 2 {
		t.Fatalf("compiles = %d, want 2", fc.callCount())
	}
	assertContains(t, screen(m), "core 3.0.71", "4 actions")
}

func TestCompilingStatusShowsReason(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	fe := m.deps.Engine.(*fakeEngine)
	fe.begin(engine.ReasonFileChanged) // and don't finish
	next, _ := m.Update(fe.drain()[0])
	assertContains(t, screen(next.(Model)), "compiling… (file changed)")
}

func TestCompileAnimation(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile)})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40}) // compile not run yet
	m = next.(Model)
	view := screen(m)
	assertContains(t, view, "compiling project", "waiting for compile…", "⠋ compiling…")
	assertNotContains(t, view, "no actions", "no action selected")

	next, _ = m.Update(animTickMsg{})
	m = next.(Model)
	if m.animFrame != 1 {
		t.Fatalf("animFrame = %d, want 1", m.animFrame)
	}
	assertContains(t, screen(m), "⠙ compiling project")

	m = drive(t, m, nil) // the engine's states arrive
	if _, cmd := m.Update(animTickMsg{}); cmd != nil {
		t.Fatal("animation kept ticking after the compile finished")
	}
	assertNotContains(t, screen(m), "compiling project", "waiting for compile")
	assertContains(t, screen(m), "find action")
}

func TestNoticeShownAtStartup(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Notice: "file watching is off"})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 36})
	assertContains(t, screen(m), "file watching is off")
	m = drive(t, m, nil, press("j")...)
	assertNotContains(t, screen(m), "file watching is off")
}

func TestWatchOffNotice(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	st := m.deps.Engine.(*fakeEngine).state
	st.WatchErr = errors.New("too many open files")
	m = drive(t, m, nil, engineMsg{st})
	assertContains(t, screen(m), "file watching is off (too many open files); press c to recompile")
	m = drive(t, m, nil, press("j")...)
	m = drive(t, m, nil, engineMsg{st})
	assertNotContains(t, screen(m), "file watching is off")
}

func TestHelpOverlay(t *testing.T) {
	m, fc := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("?")...)
	assertContains(t, screen(m), "help · esc closes", "find action", "2 lineage", "show/hide lineage", "backspace")
	// Keys other than close are swallowed while help is open.
	m = drive(t, m, nil, press("c")...)
	if fc.callCount() != 1 {
		t.Error("c recompiled while help was open")
	}
	m = drive(t, m, nil, press("esc")...)
	assertNotContains(t, screen(m), "help · esc closes")
}

func TestRunPaneTogglesAndCommand(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	view := screen(m)
	assertContains(t, view, "3 run", "[ ] d deps  [ ] D dependents  [ ] f full-refresh  [ ] s selected only", "r ▸ run 3 actions")
	m = drive(t, m, nil, press("3", "d", "f")...)
	view = screen(m)
	assertContains(t, view, "[x] d deps  [ ] D dependents  [x] f full-refresh",
		"--actions demo-project.analytics.daily_orders demo-project.analytics_assertions.")
	// s narrows r to the selected action.
	m = drive(t, m, nil, press("s")...)
	assertContains(t, screen(m), "[x] s selected only", "r ▸ run analytics.daily_orders",
		"--actions demo-project.analytics.daily_orders --include-deps --full-refresh")
	m = drive(t, m, nil, press("?")...)
	assertContains(t, screen(m), "run selected action only")
	m = drive(t, m, nil, press("esc")...)
	m = drive(t, m, nil, press("D", "d")...)
	assertContains(t, screen(m), "[ ] d deps  [x] D dependents")
}

func TestRunStreamsIntoLog(t *testing.T) {
	fr := &fakeRunner{
		lines: []string{"Compiling...", "Running: demo-project.analytics.daily_orders", "done"},
		res:   project.RunResult{ExitCode: 0, Duration: 1500 * time.Millisecond},
	}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	assertNotContains(t, screen(m), "4 log")
	m = drive(t, m, nil, press("s", "d", "r")...)
	got := fr.calls()
	want := project.RunOptions{Actions: []project.ActionID{"demo-project.analytics.daily_orders"}, IncludeDeps: true}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
	view := screen(m)
	assertContains(t, view,
		"4 log · analytics.daily_orders · ✓ exit 0 · 1.5s",
		"$ dataform run --actions demo-project.analytics.daily_orders --include-deps",
		"Running: demo-project.analytics.daily_orders", "✓ exit 0 · 1.5s", "idle",
	)
	if m.running {
		t.Error("still running after the run finished")
	}
	// L hides and shows the log, and the footer says which it will do.
	assertContains(t, view, "L hide log")
	m = drive(t, m, nil, press("L")...)
	assertNotContains(t, screen(m), "4 log")
	assertContains(t, screen(m), "L show log")
	m = drive(t, m, nil, press("L")...)
	assertContains(t, screen(m), "4 log", "L hide log")
	if m.focus != paneLog {
		t.Errorf("focus = %v after L shows the log, want the log", m.focus)
	}
}

func TestRunFailureAndStartError(t *testing.T) {
	fr := &fakeRunner{lines: []string{"boom"}, res: project.RunResult{ExitCode: 1, Duration: time.Second}}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, press("r")...)
	assertContains(t, screen(m), "✗ exit 1 · 1.0s", "boom")

	m2, fc := newTestModel(t, "basic.json")
	_ = fc
	m2 = startOn(t, m2, "demo-project.analytics.daily_orders")
	m2.deps.StartRun = func(context.Context, project.RunOptions) (*project.Run, error) {
		return nil, errors.New("exec: no such file")
	}
	m2 = drive(t, m2, nil, press("r")...)
	assertContains(t, screen(m2), "could not start dataform: exec: no such file", "failed to start")
	if m2.running {
		t.Error("running after a failed start")
	}
}

func TestRunRefusesDeclarations(t *testing.T) {
	fr := &fakeRunner{}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "raw-project.shop.orders")
	assertContains(t, screen(m), "declarations can't be run")
	m = drive(t, m, nil, press("r")...)
	assertContains(t, screen(m), "declarations can't be run")
	if len(fr.calls()) != 0 {
		t.Error("ran a declaration")
	}
}

func TestFullRefreshAsksFirst(t *testing.T) {
	fr := &fakeRunner{res: project.RunResult{Duration: time.Second}}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, press("f", "d", "r")...)
	if len(fr.calls()) != 0 || m.running {
		t.Fatal("r ran a full refresh without asking")
	}
	assertContains(t, screen(m), "confirm full refresh", "Full refresh drops and rebuilds:",
		"analytics.daily_orders", "analytics_assertions.analytics_daily_orders_assertions_rowConditions",
		"analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0", "+ their deps", "y run full refresh")

	// Any other key cancels, and doesn't do what it normally would.
	m = drive(t, m, nil, press("q")...)
	if len(fr.calls()) != 0 || m.confirmFull != nil {
		t.Fatal("q did not cancel the full refresh prompt")
	}
	assertContains(t, screen(m), "full refresh cancelled")

	// ctrl+c cancels the prompt rather than quitting.
	m = drive(t, m, nil, press("r")...)
	next, cmd := m.Update(keyMsg("ctrl+c"))
	m = next.(Model)
	if cmd != nil || m.confirmFull != nil {
		t.Fatal("ctrl+c did not just cancel the full refresh prompt")
	}

	m = drive(t, m, nil, press("r", "y")...)
	got := fr.calls()
	want := project.RunOptions{Actions: []project.ActionID{"demo-project.analytics.daily_orders",
		"demo-project.analytics_assertions.analytics_daily_orders_assertions_rowConditions",
		"demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0"}, IncludeDeps: true, FullRefresh: true}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
	assertContains(t, screen(m), "4 log · 3 actions · ✓ exit 0")

	// With selected only, the prompt names the one action.
	m = drive(t, m, nil, press("s", "r")...)
	assertContains(t, screen(m), "analytics.daily_orders + its deps")
	assertNotContains(t, screen(m), "+ their deps")
}

// TestRunRunsTheFile: r runs every runnable action the Actions pane lists,
// whichever of them is selected, and s narrows it to the selected one.
func TestRunRunsTheFile(t *testing.T) {
	fr := &fakeRunner{res: project.RunResult{Duration: time.Second}}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, press("]")...)
	if m.selected.Kind != project.KindAssertion {
		t.Fatalf("] selected a %v", m.selected.Kind)
	}
	assertContains(t, screen(m), "r ▸ run 3 actions")
	m = drive(t, m, nil, press("r")...)
	want := project.RunOptions{Actions: []project.ActionID{"demo-project.analytics.daily_orders",
		"demo-project.analytics_assertions.analytics_daily_orders_assertions_rowConditions",
		"demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0"}}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
	assertContains(t, screen(m), "4 log · 3 actions · ✓ exit 0")

	m = drive(t, m, nil, press("s", "r")...)
	want = project.RunOptions{Actions: []project.ActionID{m.selected.ID}}
	if got := fr.calls(); len(got) != 2 || !reflect.DeepEqual(got[1], want) {
		t.Fatalf("runs = %+v, want the second %+v", got, want)
	}

	// A file's actions that can't be run are left out, and with selected
	// only one of them is refused as before.
	m.graph.Actions[m.selected.ID].NoRun = true
	assertContains(t, screen(m), "assertions can't be run")
	m = drive(t, m, nil, press("r")...)
	if len(fr.calls()) != 2 {
		t.Fatal("ran an action that can't be run")
	}
	m = drive(t, m, nil, press("s", "r")...)
	if got := fr.calls(); len(got) != 3 || len(got[2].Actions) != 2 || slices.Contains(got[2].Actions, m.selected.ID) {
		t.Fatalf("runs = %+v, want the file's other two actions", got)
	}
}

func TestOneRunAtATime(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	// Pretend a run is in flight without finishing it.
	next, _ := m.Update(keyMsg("r"))
	m = next.(Model)
	if !m.running {
		t.Fatal("r did not start a run")
	}
	next, cmd := m.Update(keyMsg("r"))
	m = next.(Model)
	if cmd != nil {
		t.Error("second r started another run")
	}
	assertContains(t, screen(m), "a run is already in progress", "running… ctrl+c cancels", "▶ running 3 actions")
}

func TestCtrlCCancelsRunInsteadOfQuitting(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	canceled := false
	next, _ := m.Update(keyMsg("r"))
	m = next.(Model)
	inner := m.cancelRun
	m.cancelRun = func() { canceled = true; inner() }
	next, cmd := m.Update(keyMsg("ctrl+c"))
	m = next.(Model)
	if !canceled {
		t.Error("ctrl+c did not cancel the run")
	}
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Error("ctrl+c quit during a run")
		}
	}
	assertContains(t, screen(m), "cancelling…", "ctrl+c force-kills", "dataform is stopping", "BigQuery jobs it already started keep running")
	if !m.running {
		t.Error("run marked finished before dataform exited")
	}
}

func TestQuitDuringRunAsksFirst(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	next, _ := m.Update(keyMsg("r"))
	m = next.(Model)
	next, cmd := m.Update(keyMsg("q"))
	m = next.(Model)
	if cmd != nil {
		t.Fatal("q quit straight away during a run")
	}
	assertContains(t, screen(m), "q again or y cancels it and quits")
	next, cmd = m.Update(keyMsg("j")) // any other key stays
	m = next.(Model)
	if cmd != nil || m.confirmQuit {
		t.Fatal("other key did not dismiss the quit prompt")
	}
	next, _ = m.Update(keyMsg("q"))
	m = next.(Model)
	_, cmd = m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q q did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q q did not quit")
	}
}

// TestQuitAsksFirst: q only asks, any other key stays, and q or y then quits.
func TestQuitAsksFirst(t *testing.T) {
	for _, confirm := range []string{"q", "y"} {
		m, _ := newTestModel(t, "basic.json")
		m = startOn(t, m, "demo-project.analytics.daily_orders")
		next, cmd := m.Update(keyMsg("q"))
		m = next.(Model)
		if cmd != nil {
			t.Fatal("q quit without asking")
		}
		assertContains(t, screen(m), "quit xf? q again or y quits, any other key stays")
		next, cmd = m.Update(keyMsg("j"))
		m = next.(Model)
		if cmd != nil || m.confirmQuit {
			t.Fatal("other key did not dismiss the quit prompt")
		}
		assertNotContains(t, screen(m), "quit xf?")
		next, _ = m.Update(keyMsg("q"))
		_, cmd = next.(Model).Update(keyMsg(confirm))
		if cmd == nil {
			t.Fatalf("q %s did not quit", confirm)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("q %s did not quit", confirm)
		}
	}
}

func TestLogFollowsTailUntilScrolledUp(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line "+strings.Repeat("x", i%3)+string(rune('a'+i%26)))
	}
	fr := &fakeRunner{lines: lines, res: project.RunResult{Duration: time.Second}}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, press("r")...)
	assertContains(t, screen(m), "✓ exit 0") // tail visible
	m = drive(t, m, nil, press("4", "k", "k")...)
	if m.log.follow {
		t.Error("still following after scrolling up")
	}
	if vis := m.log.visible(); strings.Contains(ansi.Strip(vis[len(vis)-1]), "✓ exit 0") {
		t.Error("tail still visible after scrolling up")
	}
	m = drive(t, m, nil, press("G")...)
	if !m.log.follow {
		t.Error("G did not resume following")
	}
}

// TestProgramRunCancel runs a long run through the real event loop and
// cancels it with ctrl+c.
func TestProgramRunCancel(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	started := make(chan struct{})
	start := func(ctx context.Context, opts project.RunOptions) (*project.Run, error) {
		lines := make(chan string)
		done := make(chan project.RunResult, 1)
		go func() {
			lines <- "working"
			close(started)
			<-ctx.Done()
			lines <- "interrupted"
			close(lines)
			done <- project.RunResult{ExitCode: 130, Canceled: true, Duration: time.Second}
		}()
		return &project.Run{Lines: lines, Done: done}, nil
	}
	m := New(Deps{Root: "/work/demo", Engine: realEngine(t, fc.compile, nil), StartRun: start})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), "daily_orders")
	}, teatest.WithDuration(5*time.Second))
	for _, msg := range typeText("daily_orders") { // into the palette the compile opened
		tm.Send(msg)
	}
	tm.Send(keyMsg("enter"))
	tm.Send(keyMsg("r"))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	tm.Send(keyMsg("ctrl+c"))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), "cancelled")
	}, teatest.WithDuration(5*time.Second))
	// Now idle, so ctrl+c quits.
	tm.Send(keyMsg("ctrl+c"))
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(Model)
	if final.running {
		t.Error("still running at exit")
	}
	if !strings.Contains(strings.Join(final.log.lines, "\n"), "interrupted") {
		t.Errorf("log missing output after cancel: %q", final.log.lines)
	}
}

func editorEvent(event, file string) editorMsg {
	return editorMsg{project.EditorEvent{Event: event, File: file}}
}

// newFollowingModel is a test model that follows an editor. The editor channel
// never delivers; tests inject editorMsg directly.
func newFollowingModel(t *testing.T, fixtures ...string) Model {
	t.Helper()
	fc := &fakeCompiler{t: t, fixtures: fixtures}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), StartRun: (&fakeRunner{}).start, Editor: make(chan project.EditorEvent)})
	return closePalette(t, drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40}))
}

func TestEditorSelectsFileAction(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	assertContains(t, screen(m), "following nvim")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	if m.selected == nil || m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Fatalf("selected = %v, want the file's table", m.selected)
	}
	assertContains(t, screen(m), "0 sql · analytics.daily_orders")

	// Another file switches the selection; a declaration file works too.
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/customers_source.js"))
	assertContains(t, screen(m), "0 sql · shop.customers")

	// backspace undoes the switch.
	m = drive(t, m, nil, press("2", "backspace")...)
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("after backspace selected = %v", m.selected.ID)
	}
}

func TestEditorFileWithoutActionsKeepsSelection(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/stg_orders.sqlx"))
	m = drive(t, m, nil, editorEvent("BufEnter", "includes/helpers.js"))
	assertContains(t, screen(m), "includes/helpers.js: no actions", "0 sql · staging.stg_orders")
	if m.selected.ID != "demo-project.staging.stg_orders" {
		t.Errorf("selected = %v", m.selected.ID)
	}
}

func TestEditorCursorHoldDoesNotOverrideBrowsing(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	m = find(t, m, "weekly") // browse elsewhere
	m = drive(t, m, nil, editorEvent("CursorHold", "definitions/daily_orders.sqlx"))
	if m.selected.ID != "demo-project.analytics.weekly_revenue" {
		t.Errorf("CursorHold for the same file moved the selection to %v", m.selected.ID)
	}
	// Entering the buffer again does apply.
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("BufEnter selected %v", m.selected.ID)
	}
	// A CursorHold for a new file applies (e.g. xf started after nvim).
	m = drive(t, m, nil, editorEvent("CursorHold", "definitions/stg_orders.sqlx"))
	if m.selected.ID != "demo-project.staging.stg_orders" {
		t.Errorf("CursorHold for a new file selected %v", m.selected.ID)
	}
}

func TestEditorSameFileKeepsCursor(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	m = find(t, m, "uniqueKey") // an assertion from the same file
	sel := m.selected.ID
	m = drive(t, m, nil, editorEvent("FocusGained", "definitions/daily_orders.sqlx"))
	if m.selected.ID != sel {
		t.Errorf("re-entering the same file reset the selection to %v", m.selected.ID)
	}
}

func TestFollowToggle(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, press("F")...)
	assertContains(t, screen(m), "follow paused (F)")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/weekly_revenue.sqlx"))
	if m.selected != nil {
		t.Fatal("followed the editor while paused")
	}
	m = drive(t, m, nil, press("F")...)
	assertContains(t, screen(m), "following nvim")
	if m.selected == nil || m.selected.ID != "demo-project.analytics.weekly_revenue" {
		t.Errorf("resuming did not catch up with the editor: %v", m.selected)
	}
}

func TestFollowWithoutEditor(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	assertNotContains(t, screen(m), "following nvim")
	m = drive(t, m, nil, press("F")...)
	assertContains(t, screen(m), "not following an editor")
}

func TestEditorEventBeforeFirstCompile(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	fe := newFakeEngine(fc.compile)
	fe.hold = true
	m := New(Deps{Root: "/work/demo", Engine: fe, Editor: make(chan project.EditorEvent)})
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 120, Height: 40}, editorEvent("BufEnter", "definitions/stg_customers.sqlx"))
	fe.hold = false
	m = drive(t, m, m.Init())
	if m.selected == nil || m.selected.ID != "demo-project.staging.stg_customers" {
		t.Errorf("selected = %v, want the file reported before the compile finished", m.selected)
	}
	if m.palette.open {
		t.Error("palette opened although the editor's file selected an action")
	}
}

func TestEditorSelectionSurvivesRecompile(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	m = drive(t, m, nil, press("c")...)
	if m.selected == nil || m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("selected = %v", m.selected)
	}
}

func TestPaletteRanksEditorFileFirst(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = find(t, m, "weekly")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	m = drive(t, m, nil, press("/")...)
	// The current action (the file's table), then the file's assertions, then
	// recent actions.
	var files []string
	for _, it := range m.palette.list[:4] {
		files = append(files, it.item.Action.FileName)
	}
	want := []string{"definitions/daily_orders.sqlx", "definitions/daily_orders.sqlx", "definitions/daily_orders.sqlx", "definitions/weekly_revenue.sqlx"}
	if fmt.Sprint(files) != fmt.Sprint(want) {
		t.Errorf("first rows from %v, want %v", files, want)
	}
	if m.palette.cursor != 1 {
		t.Errorf("cursor = %d, want the row after the current action", m.palette.cursor)
	}
}

// TestProgramEditor sends editor events through the real event loop.
func TestProgramEditor(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	editor := make(chan project.EditorEvent)
	m := New(Deps{Root: "/work/demo", Engine: realEngine(t, fc.compile, nil), Editor: editor})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), "daily_orders")
	}, teatest.WithDuration(5*time.Second))
	editor <- project.EditorEvent{Event: "BufEnter", File: "definitions/stg_orders.sqlx"}
	editor <- project.EditorEvent{Event: "BufEnter", File: "definitions/weekly_revenue.sqlx"}
	// Only the changed cells are redrawn, so look for the file's name alone:
	// the palette labels leave the extension out, so this is the Actions pane.
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), "weekly_revenue.sqlx")
	}, teatest.WithDuration(5*time.Second))
	tm.Send(keyMsg("esc")) // the palette the compile opened
	tm.Send(keyMsg("q"))
	tm.Send(keyMsg("q")) // q asks, q again quits
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(Model)
	if final.selected == nil || final.selected.ID != "demo-project.analytics.weekly_revenue" {
		t.Errorf("selected = %v", final.selected)
	}
}

// paneTitleRows returns the screen row and column where each pane's top
// border ("╭─0 sql…", "╭─▸ 2 lineage…") starts.
func paneTitleRows(view string) map[string][2]int {
	out := map[string][2]int{}
	for row, line := range strings.Split(view, "\n") {
		for col := strings.Index(line, "╭─"); col >= 0; {
			rest := strings.TrimPrefix(strings.TrimPrefix(line[col+len("╭─"):], "▸ "), "▾ ")
			for _, title := range []string{"1 actions", "2 lineage", "3 run", "0 sql", "4 log"} {
				if strings.HasPrefix(rest, title) {
					out[title] = [2]int{row, len([]rune(line[:col]))}
				}
			}
			next := strings.Index(line[col+1:], "╭─")
			if next < 0 {
				break
			}
			col += 1 + next
		}
	}
	return out
}

func TestLayout(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 90, Height: 48})
	view := screen(m)
	pos := paneTitleRows(view)
	order := []string{"1 actions", "2 lineage", "3 run", "0 sql"}
	for i, title := range order {
		p, ok := pos[title]
		if !ok {
			t.Fatalf("pane %q missing:\n%s", title, view)
		}
		if p[1] != 0 {
			t.Errorf("pane %q starts at column %d, want full width", title, p[1])
		}
		if i > 0 && p[0] <= pos[order[i-1]][0] {
			t.Errorf("pane %q (row %d) is not below %q (row %d)", title, p[0], order[i-1], pos[order[i-1]][0])
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(line); w > 90 {
			t.Errorf("line wider than the terminal (%d): %q", w, line)
		}
	}
	if n := len(strings.Split(view, "\n")); n != 48 {
		t.Errorf("view has %d lines, want 48", n)
	}
}

func TestLineageCollapsesAndExpands(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 90, Height: 48})
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	closed := m.geometry()
	if closed.lineageH != lineageClosedH {
		t.Fatalf("collapsed lineage height = %d", closed.lineageH)
	}
	assertContains(t, screen(m), "▸ 2 lineage · ↑2 ↓4", "O opens")
	assertNotContains(t, screen(m), "upstream (2)")

	// O expands it to fit its content, leaving the focus alone.
	m = drive(t, m, nil, press("O")...)
	open := m.geometry()
	if want := len(m.lineage.rows) + 2; open.lineageH != want {
		t.Errorf("expanded lineage height = %d, want %d", open.lineageH, want)
	}
	if open.sqlH != closed.sqlH-(open.lineageH-closed.lineageH) {
		t.Errorf("sql pane did not give up the rows: %d -> %d", closed.sqlH, open.sqlH)
	}
	if m.sql.height != open.sqlH-2 || m.lineage.height != open.lineageH-2 {
		t.Errorf("scroller heights %d/%d do not match the panes", m.sql.height, m.lineage.height)
	}
	assertContains(t, screen(m), "▾ 2 lineage", "upstream (2)")
	if m.focus != paneSQL {
		t.Errorf("O moved the focus to %v", m.focus)
	}

	// It stays open while focus moves; O while focused collapses it and
	// focuses the SQL.
	m = drive(t, m, nil, press("2", "0")...)
	if !m.lineageOpen {
		t.Error("lineage collapsed when it lost focus")
	}
	m = drive(t, m, nil, press("2", "O")...)
	if m.lineageOpen || m.focus != paneSQL {
		t.Errorf("after 1 O: open = %v, focus = %v", m.lineageOpen, m.focus)
	}
}

func TestStackedLayoutWithLog(t *testing.T) {
	fr := &fakeRunner{lines: []string{"hello"}, res: project.RunResult{Duration: time.Second}}
	m, _, _ := newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 80, Height: 40})
	m = drive(t, m, nil, press("r")...)
	pos := paneTitleRows(screen(m))
	if pos["3 run"][0] >= pos["0 sql"][0] || pos["0 sql"][0] >= pos["4 log"][0] {
		t.Errorf("want run above sql above log, got %v", pos)
	}
	if n := len(strings.Split(screen(m), "\n")); n != 40 {
		t.Errorf("view has %d lines, want 40", n)
	}
}

func TestNarrowHelpFits(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 60, Height: 64}, keyMsg("?"))
	view := screen(m)
	assertContains(t, view, "global", "2 lineage", "backspace")
	for _, line := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(line); w > 60 {
			t.Errorf("line wider than the terminal (%d): %q", w, line)
		}
	}
}

func TestSecondCtrlCForceKills(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	var killed bool // set on the test goroutine (Update calls Kill directly)
	ctxDone := make(chan struct{})
	m.deps.StartRun = func(ctx context.Context, _ project.RunOptions) (*project.Run, error) {
		go func() { <-ctx.Done(); close(ctxDone) }()
		return &project.Run{Lines: make(chan string), Done: make(chan project.RunResult), Kill: func() { killed = true }}, nil
	}
	next, cmd := m.Update(keyMsg("r"))
	m = next.(Model)
	next, _ = m.Update(cmd()) // runStartedMsg; don't wait for lines
	m = next.(Model)

	next, _ = m.Update(keyMsg("ctrl+c"))
	m = next.(Model)
	select {
	case <-ctxDone:
	case <-time.After(time.Second):
		t.Fatal("first ctrl+c did not cancel the run context")
	}
	if killed {
		t.Fatal("first ctrl+c force-killed; want a graceful cancel only")
	}
	assertContains(t, screen(m), "ctrl+c force-kill run")

	next, cmd = m.Update(keyMsg("ctrl+c"))
	m = next.(Model)
	if !killed {
		t.Error("second ctrl+c did not force-kill")
	}
	if cmd != nil {
		t.Error("second ctrl+c during a run returned a command (quit?)")
	}
	assertContains(t, screen(m), "force-killed")
}

func TestZoom(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("0", "z")...)
	view := screen(m)
	pos := paneTitleRows(view)
	if _, ok := pos["0 sql"]; !ok || len(pos) != 1 {
		t.Fatalf("want only the SQL pane when zoomed, got %v", pos)
	}
	assertContains(t, view, "zoom (esc)", "esc unzoom")
	if want := m.geometry().bodyH - 2; m.sql.height != want {
		t.Errorf("zoomed sql height = %d, want %d", m.sql.height, want)
	}
	// Changing focus zooms the newly focused pane.
	m = drive(t, m, nil, press("2")...)
	if pos := paneTitleRows(screen(m)); len(pos) != 1 || !m.lineageOpen {
		t.Errorf("want the lineage pane zoomed, got %v", pos)
	}
	m = drive(t, m, nil, press("esc")...)
	if len(paneTitleRows(screen(m))) < 4 {
		t.Error("esc did not restore the panes")
	}
	// z toggles too.
	m = drive(t, m, nil, press("z", "z")...)
	if m.zoomed {
		t.Error("z z left the pane zoomed")
	}
}

func TestPaletteUnzooms(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("z", "/")...)
	if m.zoomed || !m.palette.open {
		t.Fatalf("zoomed = %v, palette open = %v", m.zoomed, m.palette.open)
	}
	// esc closes the palette without doing anything else.
	m = drive(t, m, nil, press("esc")...)
	if m.palette.open {
		t.Error("esc did not close the palette")
	}
}

func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func wheel(x, y int, down bool) tea.MouseWheelMsg {
	b := tea.MouseWheelUp
	if down {
		b = tea.MouseWheelDown
	}
	return tea.MouseWheelMsg{X: x, Y: y, Button: b}
}

func rectOf(t *testing.T, m Model, id paneID) paneRect {
	t.Helper()
	for _, r := range m.paneRects() {
		if r.id == id {
			return r
		}
	}
	t.Fatalf("pane %v not visible", id)
	return paneRect{}
}

func TestPaneRectsMatchRender(t *testing.T) {
	for _, open := range []bool{false, true} {
		m, _ := newTestModel(t, "basic.json")
		m = startOn(t, m, "demo-project.analytics.daily_orders")
		m.lineageOpen = open
		m = drive(t, m, nil, press("r")...) // show the log pane too
		view := paneTitleRows(screen(m))
		names := map[paneID]string{paneLineage: "2 lineage", paneSQL: "0 sql", paneRun: "3 run", paneLog: "4 log"}
		for _, r := range m.paneRects() {
			got := view[names[r.id]]
			if got[0] != r.y || got[1] != r.x {
				t.Errorf("lineage open %v: %s drawn at row %d col %d, rect says row %d col %d", open, names[r.id], got[0], got[1], r.y, r.x)
			}
		}
	}
}

func TestClickFocusesAndSelects(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	run := rectOf(t, m, paneRun)
	m = drive(t, m, nil, click(run.x+5, run.y+1))
	if m.focus != paneRun {
		t.Fatalf("focus = %v after clicking the Run pane", m.focus)
	}

	// Lineage: clicking it collapsed expands it; then the first click on an
	// entry selects it and a second click on the same entry jumps.
	lin := rectOf(t, m, paneLineage)
	m = drive(t, m, nil, click(lin.x+4, lin.y))
	if m.focus != paneLineage || !m.lineageOpen {
		t.Fatalf("focus = %v, open = %v after clicking collapsed lineage", m.focus, m.lineageOpen)
	}
	lin = rectOf(t, m, paneLineage)
	m = drive(t, m, nil, click(lin.x+4, lin.y+1+2)) // "└ staging.stg_orders"
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Fatalf("first click jumped: selected = %v", m.selected.ID)
	}
	m = drive(t, m, nil, click(lin.x+4, lin.y+1+2))
	if m.selected.ID != "demo-project.staging.stg_orders" {
		t.Errorf("second click did not jump: selected = %v", m.selected.ID)
	}
}

func TestClickPalette(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("/")...)
	b := m.palette.box(m.width, m.geometry().bodyH)
	// Rows: analytics.daily_orders, analytics.refresh_permissions, …
	m = drive(t, m, nil, click(b.x+4, b.y+2))
	if m.palette.open || m.selected == nil || m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Fatalf("open = %v, selected = %v", m.palette.open, m.selected)
	}
	// Clicking outside closes it.
	m = drive(t, m, nil, press("/")...)
	m = drive(t, m, nil, click(0, 0))
	if m.palette.open || m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("open = %v, selected = %v after clicking outside", m.palette.open, m.selected.ID)
	}
}

func TestWheelScrollsPaneUnderPointer(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("/")...)
	m = drive(t, m, nil, wheel(10, 10, true), wheel(10, 10, true))
	if m.palette.cursor != 2 {
		t.Errorf("wheel with the palette open: cursor = %d", m.palette.cursor)
	}

	fr := &fakeRunner{res: project.RunResult{Duration: time.Second}}
	for i := 0; i < 80; i++ {
		fr.lines = append(fr.lines, "line")
	}
	m, _, _ = newTestModelWithRunner(t, fr, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, press("r")...)
	log := rectOf(t, m, paneLog)
	m = drive(t, m, nil, wheel(log.x+3, log.y+2, false))
	if m.log.follow {
		t.Error("wheel up over the log did not stop following")
	}
}

func TestMouseIgnoredUnderOverlay(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press("3", "?")...)
	sql := rectOf(t, m, paneSQL)
	m = drive(t, m, nil, click(sql.x+5, sql.y+3))
	if m.focus == paneSQL {
		t.Error("click went through the help overlay")
	}
}

// hubGraph is a project where p.s.hub (in definitions/hub.sqlx) reads from
// p.s.src and has n dependents.
func hubGraph(t *testing.T, n int) func(context.Context) (*project.Graph, error) {
	t.Helper()
	tables := []string{
		`{"type":"table","target":{"database":"p","schema":"s","name":"src"},"fileName":"definitions/src.sqlx","query":"select 1"}`,
		`{"type":"table","target":{"database":"p","schema":"s","name":"hub"},"fileName":"definitions/hub.sqlx","query":"select * from src",
		  "dependencyTargets":[{"database":"p","schema":"s","name":"src"}]}`,
	}
	for i := 0; i < n; i++ {
		tables = append(tables, fmt.Sprintf(`{"type":"view","target":{"database":"p","schema":"s","name":"dep_%02d"},"fileName":"definitions/dep_%02d.sqlx","query":"select 1",
		  "dependencyTargets":[{"database":"p","schema":"s","name":"hub"}]}`, i, i))
	}
	data := []byte(`{"tables":[` + strings.Join(tables, ",") + `]}`)
	return func(context.Context) (*project.Graph, error) { return dataform.ParseCompileJSON(data) }
}

func newHubModel(t *testing.T, dependents, width, height int) Model {
	t.Helper()
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(hubGraph(t, dependents)), Editor: make(chan project.EditorEvent)})
	m = closePalette(t, drive(t, m, m.Init(), tea.WindowSizeMsg{Width: width, Height: height}))
	return drive(t, m, nil, editorEvent("BufEnter", "definitions/hub.sqlx"))
}

func TestExpandedLineageFitsContent(t *testing.T) {
	// 1 upstream and 11 downstream entries fit in 40% of a 50 row screen.
	m := newHubModel(t, 11, 90, 50)
	m = drive(t, m, nil, press("O")...)
	view := lineageView(m)
	assertContains(t, view, "upstream (1)", "downstream (11)", "s.dep_00", "s.dep_10")
	if above, below := m.lineage.hiddenEntries(); above+below != 0 {
		t.Errorf("%d lineage entries hidden", above+below)
	}
	assertNotContains(t, screen(m), "more ─")
	if g := m.geometry(); g.sqlH < 15 {
		t.Errorf("sql pane squeezed to %d rows", g.sqlH)
	}
}

func TestExpandedLineageOverflows(t *testing.T) {
	m := newHubModel(t, 40, 90, 48)
	m = drive(t, m, nil, press("2")...)
	g := m.geometry()
	if max := (g.bodyH - g.cardH - g.runH) * 4 / 10; g.lineageH > max {
		t.Errorf("lineage height %d above 40%% (%d)", g.lineageH, max)
	}
	if m.lineage.height != g.lineageH-2 {
		t.Errorf("lineage scroll height %d does not match its pane (%d)", m.lineage.height, g.lineageH-2)
	}
	_, below := m.lineage.hiddenEntries()
	if below == 0 {
		t.Fatal("expected a 40-entry lineage to overflow")
	}
	assertContains(t, screen(m), fmt.Sprintf("↓ %d more", below))
	// Scrolling to the end shows an "above" count and hides the "below" one.
	m = drive(t, m, nil, press("G")...)
	above, below := m.lineage.hiddenEntries()
	if above == 0 || below != 0 {
		t.Errorf("after G: above=%d below=%d", above, below)
	}
	assertContains(t, screen(m), fmt.Sprintf("↑ %d more", above), "s.dep_39")
}

// TestProgram runs the model through Bubble Tea's real event loop.
func TestProgram(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: "/work/demo", Engine: realEngine(t, fc.compile, nil)})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 36))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), "daily_orders")
	}, teatest.WithDuration(5*time.Second))
	for _, msg := range typeText("refresh") { // into the palette the compile opened
		tm.Send(msg)
	}
	tm.Send(keyMsg("enter"))
	tm.Send(keyMsg("q"))
	tm.Send(keyMsg("q")) // q asks, q again quits
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(Model)
	if final.selected == nil || final.selected.ID != "demo-project.analytics.refresh_permissions" {
		t.Errorf("selected = %v", final.selected)
	}
}

// TestProgramWatch checks that watch notifications reach the event loop and
// the wait command is re-armed after each one.
func TestProgramWatch(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	watch := make(chan struct{})
	m := New(Deps{Root: "/work/demo", Engine: realEngine(t, fc.compile, watch)})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 36))
	waitCalls := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for fc.callCount() < n {
			if time.Now().After(deadline) {
				t.Fatalf("compiles = %d, want %d", fc.callCount(), n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitCalls(1)
	// The first compile's result opens the palette; on a slow machine it can
	// arrive after the keys below, which would then type into it.
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), "daily_orders")
	}, teatest.WithDuration(5*time.Second))
	watch <- struct{}{}
	waitCalls(2)
	watch <- struct{}{}
	waitCalls(3)
	tm.Send(keyMsg("esc")) // the palette the compile opened
	tm.Send(keyMsg("q"))
	tm.Send(keyMsg("q")) // q asks, q again quits
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

func TestActionsPaneListsFileActions(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	// Every action of the file has a row and its details, the table first
	// and selected, the kinds padded to line the names up.
	view := screen(m)
	assertContains(t, view, "1 actions · definitions/daily_orders.sqlx",
		"▸ TABLE      demo-project.analytics.daily_orders",
		"  ASSERTION  demo-project.analytics_assertions.analytics_daily_orders_assertions_rowConditions",
		"  ASSERTION  demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0",
		"[/] file actions")
	if g := m.geometry(); g.cardH != 3+2 {
		t.Errorf("actions pane height = %d, want its three rows", g.cardH)
	}

	// ] and [ step through them anywhere, wrapping around.
	m = drive(t, m, nil, press("2", "]")...)
	if m.selected.Kind != project.KindAssertion || m.selected.FileName != "definitions/daily_orders.sqlx" {
		t.Fatalf("] selected %v", m.selected.ID)
	}
	assertContains(t, screen(m), "▸ ASSERTION  demo-project.analytics_assertions.analytics_daily_orders_assertions_rowConditions")
	m = drive(t, m, nil, press("]", "]")...)
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("after wrapping selected = %v", m.selected.ID)
	}
	m = drive(t, m, nil, press("[")...)
	if m.selected.Kind != project.KindAssertion {
		t.Errorf("[ selected %v", m.selected.ID)
	}

	// Focused, j/k move without wrapping, g/G jump to the ends and enter
	// focuses the SQL.
	m = drive(t, m, nil, press("1", "g")...)
	if m.focus != paneActions || m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Fatalf("1 g: focus %v, selected %v", m.focus, m.selected.ID)
	}
	m = drive(t, m, nil, press("k")...)
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("k at the top selected %v", m.selected.ID)
	}
	m = drive(t, m, nil, press("j")...)
	if !strings.HasSuffix(string(m.selected.ID), "_rowConditions") {
		t.Errorf("j selected %v", m.selected.ID)
	}
	assertContains(t, screen(m), "0 sql · analytics_assertions.analytics_daily_orders_assertions_rowConditions · assertion")
	m = drive(t, m, nil, press("G", "enter")...)
	if !strings.HasSuffix(string(m.selected.ID), "_uniqueKey_0") || m.focus != paneSQL {
		t.Errorf("G enter: selected %v, focus %v", m.selected.ID, m.focus)
	}

	// A file with a single action has one row, and ] says so.
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/weekly_revenue.sqlx"))
	assertNotContains(t, screen(m), "ASSERTION")
	m = drive(t, m, nil, press("]")...)
	assertContains(t, screen(m), "no other actions in this file")
}

// A declaration is shown alone, though its file declares others, and ]
// says how to find them.
func TestDeclarationShownAlone(t *testing.T) {
	m := newFollowingModel(t, "examples/dataform.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/sources/declarations.js"))
	if m.selected == nil || m.selected.ID != "alex-personal-dev-01.raw.customers" {
		t.Fatalf("selected %v", m.selected)
	}
	if got := len(m.paneActions()); got != 1 {
		t.Errorf("Actions pane lists %d actions, want 1", got)
	}
	assertNotContains(t, screen(m), "raw.orders")
	m = drive(t, m, nil, press("]")...)
	assertContains(t, screen(m), "declarations are shown alone: / $declaration finds the others")
	if m.selected.ID != "alex-personal-dev-01.raw.customers" {
		t.Errorf("] selected %v", m.selected.ID)
	}
}

func TestClickSelectsInActionsPane(t *testing.T) {
	m := newFollowingModel(t, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	r := rectOf(t, m, paneActions)
	m = drive(t, m, nil, click(r.x+10, r.y+3)) // the third row
	if m.focus != paneActions || !strings.HasSuffix(string(m.selected.ID), "_uniqueKey_0") {
		t.Errorf("click: focus %v, selected %v", m.focus, m.selected.ID)
	}
	m = drive(t, m, nil, wheel(r.x+10, r.y+1, false))
	if !strings.HasSuffix(string(m.selected.ID), "_rowConditions") {
		t.Errorf("wheel up selected %v", m.selected.ID)
	}
}

func TestOpenInEditorNeedsAnEditor(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	assertNotContains(t, screen(m), "open in editor")
	m = drive(t, m, nil, press("o")...)
	assertContains(t, screen(m), "o needs an editor")
	m = drive(t, m, nil, press("?")...)
	assertNotContains(t, screen(m), "open in editor")
}

func TestOpenInEditor(t *testing.T) {
	var opened []string
	var fail error
	var leave bool
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow,
		Open: func(file string, line int) (Opening, error) {
			opened = append(opened, fmt.Sprintf("%s:%d", file, line))
			return Opening{Leave: leave}, fail
		}})
	m = closePalette(t, drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 200, Height: 40}))
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	assertContains(t, screen(m), "o open in editor")

	m = drive(t, m, nil, press("o")...)
	if want := []string{filepath.FromSlash("/work/demo/definitions/daily_orders.sqlx") + ":0"}; !slices.Equal(opened, want) {
		t.Fatalf("opened %q, want %q", opened, want)
	}
	assertContains(t, screen(m), "opened definitions/daily_orders.sqlx in your editor")

	// In the SQL pane's visual mode o is "other end", not open.
	m = drive(t, m, nil, press("0", "v", "o", "esc")...)
	if len(opened) != 1 {
		t.Errorf("o in visual mode opened %q", opened[1:])
	}

	fail = errors.New("no editor window")
	m = drive(t, m, nil, press("o")...)
	assertContains(t, screen(m), "could not open definitions/daily_orders.sqlx: no editor window")

	// An editor that replaces xf (neovim with xf beside it) isn't opened
	// during a run; otherwise xf leaves, saying what to open.
	fail, leave = nil, true
	m.running = true
	m = drive(t, m, nil, press("o")...)
	assertContains(t, screen(m), "a run is in progress")
	if _, _, ok := m.Leaving(); ok {
		t.Error("left during a run")
	}
	m.running = false
	m = drive(t, m, nil, press("o")...)
	if file, line, ok := m.Leaving(); !ok || file != filepath.FromSlash("/work/demo/definitions/daily_orders.sqlx") || line != 0 {
		t.Errorf("Leaving = %q, %d, %v", file, line, ok)
	}
}

// A JavaScript file opens at the line that names the action.
func TestOpenInEditorAtTheAction(t *testing.T) {
	root := t.TempDir()
	js := filepath.Join(root, "definitions", "customers_source.js")
	if err := os.MkdirAll(filepath.Dir(js), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(js, []byte("// sources\n\ndeclare({ database: \"raw-project\", schema: \"shop\", name: \"customers\" });\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var opened []string
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: root, Engine: newFakeEngine(fc.compile), Now: testNow,
		Open: func(file string, line int) (Opening, error) {
			opened = append(opened, fmt.Sprintf("%s:%d", file, line))
			return Opening{}, nil
		}})
	m = closePalette(t, drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 200, Height: 40}))
	m = startOn(t, m, "raw-project.shop.customers")
	drive(t, m, nil, press("o")...)
	if want := []string{js + ":2"}; !slices.Equal(opened, want) {
		t.Errorf("opened %q, want %q", opened, want)
	}
}

func TestFullScreen(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	assertNotContains(t, screen(m), "full screen")
	m = drive(t, m, nil, press("Z")...)
	assertContains(t, screen(m), "Z needs xf inside nvim")

	toggles := 0
	var fail error
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m = New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow,
		FullScreen: func() error { toggles++; return fail }})
	m = closePalette(t, drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 250, Height: 40}))
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	assertContains(t, screen(m), "Z full screen in nvim")
	m = drive(t, m, nil, press("Z", "0", "Z")...)
	if toggles != 2 {
		t.Errorf("Z toggled full screen %d times, want 2", toggles)
	}
	fail = errors.New("no xf window")
	m = drive(t, m, nil, press("Z")...)
	assertContains(t, screen(m), "could not toggle full screen in nvim: no xf window")
}

// In a terminal as narrow as a split beside an editor, the palette fills it
// and long names lose their start, never their end.
func TestPaletteInNarrowTerminal(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 50, Height: 30})
	m = drive(t, m, nil, append(press("/"), typeText("daily_orders")...)...)
	view := paletteView(m)
	if w := ansi.StringWidth(strings.Split(view, "\n")[0]); w != 48 {
		t.Errorf("palette width = %d, want 48", w)
	}
	assertContains(t, view, "analytics.daily_orders", "…_daily_orders_assertions_uniqueKey_0", "…aily_orders_assertions_rowConditions")
}

func TestFitName(t *testing.T) {
	for _, c := range []struct {
		width int
		want  string
	}{
		{30, "analytics.daily_orders"},
		{22, "analytics.daily_orders"},
		{21, "daily_orders"},
		{12, "daily_orders"},
		{8, "…_orders"},
		{1, "…"},
		{0, "…"},
	} {
		if got := fitName("analytics.daily_orders", "daily_orders", c.width); got != c.want {
			t.Errorf("fitName(%d) = %q, want %q", c.width, got, c.want)
		}
	}
}

func TestSlashInEmptyPaletteIsIgnored(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, append(press("/", "/"), typeText("daily/")...)...)
	if got := m.palette.input.Value(); got != "daily/" {
		t.Errorf("query = %q, want the / typed after text kept and the leading one dropped", got)
	}
}

// Beside an editor, xf shows an action after the first compile instead of
// opening the palette: the editor's file, else the first runnable action.
func TestBesideEditorShowsAnActionAtStartup(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow, Beside: true})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.palette.open {
		t.Error("palette opened beside the editor")
	}
	if m.selected == nil || m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("selected = %v, want the first runnable action by name", m.selected)
	}

	fc = &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	editor := make(chan project.EditorEvent, 1)
	editor <- project.EditorEvent{Event: "BufEnter", File: "definitions/weekly_revenue.sqlx"}
	m = New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow, Beside: true, Editor: editor})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.palette.open || m.selected == nil || m.selected.FileName != "definitions/weekly_revenue.sqlx" {
		t.Errorf("palette open %v, selected %v; want the editor's file shown", m.palette.open, m.selected)
	}
}

func TestActionsPaneOpensTableInBigQuery(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	var opened []string
	m.deps.OpenURL = func(u string) error { opened = append(opened, u); return nil }

	m = drive(t, startOn(t, m, "demo-project.analytics.daily_orders"), nil, press("1")...)
	assertContains(t, screen(m), "u BigQuery")
	m = drive(t, m, nil, press("u")...)
	want := "https://console.cloud.google.com/bigquery?project=demo-project&ws=!1m5!1m4!4m3!1sdemo-project!2sanalytics!3sdaily_orders"
	if len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened %q, want %q", opened, want)
	}

	// An operation has no table to open.
	m = drive(t, startOn(t, m, "demo-project.analytics.refresh_permissions"), nil, press("1")...)
	assertNotContains(t, screen(m), "u BigQuery")
	m = drive(t, m, nil, press("u")...)
	if len(opened) != 1 {
		t.Errorf("u opened %q for an operation", opened[1:])
	}
	assertContains(t, screen(m), "has none")

	// Help lists it only when a browser can be opened.
	m = drive(t, m, nil, press("?")...)
	assertContains(t, screen(m), "open in BigQuery")
	m.deps.OpenURL = nil
	assertNotContains(t, screen(m), "open in BigQuery")
}

func TestLineageOpensAndCopiesTable(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	var opened, copied []string
	m.deps.OpenURL = func(u string) error { opened = append(opened, u); return nil }
	m.deps.Copy = func(s string) error { copied = append(copied, s); return nil }

	// The cursor starts on the first upstream entry, stg_customers.
	m = drive(t, startOn(t, m, "demo-project.analytics.daily_orders"), nil, press("2")...)
	assertContains(t, screen(m), "u BigQuery", "y/Y copy name/path")
	m = drive(t, m, nil, press("u", "y")...)
	want := "https://console.cloud.google.com/bigquery?project=demo-project&ws=!1m5!1m4!4m3!1sdemo-project!2sstaging!3sstg_customers"
	if len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened %q, want %q", opened, want)
	}
	if len(copied) != 1 || copied[0] != "`demo-project.staging.stg_customers`" {
		t.Fatalf("copied %q", copied)
	}
	assertContains(t, screen(m), "copied `demo-project.staging.stg_customers`")
	// Y copies the relative path; again, the absolute one; again, relative.
	m = drive(t, m, nil, press("Y")...)
	assertContains(t, screen(m), "Y again copies the absolute path")
	m = drive(t, m, nil, press("Y", "Y")...)
	paths := []string{"definitions/stg_customers.sqlx", filepath.FromSlash("/work/demo/definitions/stg_customers.sqlx"), "definitions/stg_customers.sqlx"}
	if !slices.Equal(copied[1:], paths) {
		t.Fatalf("copied %q, want %q", copied[1:], paths)
	}
	if m.selected.ID != "demo-project.analytics.daily_orders" {
		t.Errorf("u/y changed the selection to %s", m.selected.ID)
	}
}

func TestActionsPaneCopiesTarget(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	var copied []string
	m.deps.Copy = func(s string) error { copied = append(copied, s); return nil }
	// Another key in between: Y copies the relative path again.
	m = drive(t, startOn(t, m, "raw-project.shop.orders"), nil, press("1", "Y", "y", "Y")...)
	want := []string{"definitions/sources.sqlx", "`raw-project.shop.orders`", "definitions/sources.sqlx"}
	if !slices.Equal(copied, want) {
		t.Fatalf("copied %q, want %q", copied, want)
	}
}

// A xf started where the last one quit (an accidental q, say) selects the
// action that one had selected instead of opening the palette, and hands
// back what to restore next time.
func TestResumesTheLastSession(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	resume := session.Session{
		Selected: "demo-project.analytics.weekly_revenue",
		Recent:   []project.ActionID{"demo-project.analytics.weekly_revenue", "demo-project.analytics.daily_orders"},
	}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow, Resume: resume})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.palette.open {
		t.Error("palette opened although there was an action to resume")
	}
	if m.selected == nil || m.selected.ID != resume.Selected {
		t.Fatalf("selected = %v, want %s", m.selected, resume.Selected)
	}
	if m.lineageOpen {
		t.Error("lineage pane open at startup, want it collapsed")
	}
	got := m.Session()
	if got.Root != "/work/demo" || got.Selected != resume.Selected ||
		!slices.Equal(got.Recent, resume.Recent) {
		t.Errorf("Session() = %+v, want the resumed session back", got)
	}

	// An action that is gone falls back to the palette.
	fc = &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m = New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow,
		Resume: session.Session{Selected: "demo-project.analytics.deleted"}})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	if !m.palette.open || m.selected != nil {
		t.Errorf("palette open %v, selected %v; want the palette and nothing selected", m.palette.open, m.selected)
	}
}

// TestNoRunPane covers a project whose actions can't be run from xf (no
// StartRun): no Run pane, no run keys, and a compile's notice shown.
func TestNoRunPane(t *testing.T) {
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	compile := func(ctx context.Context) (*project.Graph, error) {
		g, err := fc.compile(ctx)
		if err != nil {
			return nil, err
		}
		g.Notice = "SQL not compiled: hooks"
		return g, nil
	}
	m := New(Deps{Backend: "dbt", Root: "/work/demo", Target: "ci", Engine: newFakeEngine(compile), Now: testNow})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	view := screen(m)
	assertContains(t, view, "SQL not compiled: hooks", " DBT ", "demo · target ci", "dbt 3.0.43")
	m = closePalette(t, m)
	view = screen(m)
	if _, ok := paneTitleRows(view)["3 run"]; ok {
		t.Errorf("the Run pane is shown:\n%s", view)
	}
	m = drive(t, m, nil, press("3")...)
	if m.focus == paneRun {
		t.Error("3 focused the hidden Run pane")
	}
	for _, p := range m.tabOrder() {
		if p == paneRun {
			t.Error("tab cycles through the hidden Run pane")
		}
	}
	m = find(t, m, "daily_orders")
	assertContains(t, screen(m), "· not compiled")
	assertNotContains(t, screen(m), "r run")
	m = drive(t, m, nil, press("?")...)
	assertNotContains(t, screen(m), "full-refresh")
}

// TestDbtRun runs a dbt model: the footer and the log show the dbt build
// command, the run selects the model by its dbt name, and the full refresh
// prompt says what dbt rebuilds.
func TestDbtRun(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "examples", "dbt-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	compile := func(context.Context) (*project.Graph, error) { return (&dbt.Backend{}).Parse(data) }
	fr := &fakeRunner{res: project.RunResult{Duration: time.Second}}
	b := dbt.New("dbt", "ci", nil, false)
	m := New(Deps{Backend: "dbt", Root: "/work/shop", Engine: newFakeEngine(compile), StartRun: fr.start, RunCommand: func(o project.RunOptions) string { return b.RunCommand("/work/shop", o) }, Now: testNow})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 140, Height: 40})
	m = startOn(t, closePalette(t, m), "alex-personal-dev-01.xf_example.fct_orders")
	m = drive(t, m, nil, press("3")...)
	// r runs the model with the tests its pane lists.
	assertContains(t, screen(m), "$ dbt build --select xf_example.marts.fct_orders ", "r ▸ run 5 actions")
	m = drive(t, m, nil, press("s")...)
	assertContains(t, screen(m), "$ dbt build --select xf_example.marts.fct_orders --target ci", "r ▸ run xf_example.fct_orders")

	m = drive(t, m, nil, press("d", "f", "r")...)
	assertContains(t, screen(m), "Incremental models lose their existing rows and", "seeds are reloaded")
	m = drive(t, m, nil, press("y")...)
	want := project.RunOptions{Actions: []project.ActionID{"alex-personal-dev-01.xf_example.fct_orders"}, Names: []string{"xf_example.marts.fct_orders"}, IncludeDeps: true, FullRefresh: true}
	if got := fr.calls(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("runs = %+v, want [%+v]", got, want)
	}
	assertContains(t, screen(m), "$ dbt build --select +xf_example.marts.fct_orders --full-refresh --target ci", "✓ exit 0")

	// Ephemeral models are never built, so there's nothing to run.
	m = startOn(t, m, "alex-personal-dev-01.xf_example.int_customer_countries")
	m = drive(t, m, nil, press("r")...)
	assertContains(t, screen(m), "ephemerals can't be run")
	if len(fr.calls()) != 1 {
		t.Error("ran an ephemeral model")
	}
}

// Keys within Deps.StartupQuiet of starting are dropped: they are most
// likely a terminal reply's tail, such as "1u", which focused the
// Actions pane and opened its table in the browser. Later keys work.
func TestStartupQuietDropsEarlyKeys(t *testing.T) {
	now := testNow()
	var opened []string
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: func() time.Time { return now }, Beside: true,
		StartupQuiet: 500 * time.Millisecond, OpenURL: func(u string) error { opened = append(opened, u); return nil }})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 200, Height: 40})

	now = now.Add(100 * time.Millisecond)
	m = drive(t, m, nil, press("1", "u")...)
	if len(opened) != 0 {
		t.Fatalf("keys 100ms after starting opened %q", opened)
	}
	now = now.Add(time.Second)
	drive(t, m, nil, press("1", "u")...)
	if len(opened) != 1 {
		t.Errorf("keys after the quiet period opened %q, want one URL", opened)
	}
}

func infoTestModel(t *testing.T, w int, edit func(*Deps)) (Model, *string) {
	t.Helper()
	copied := new(string)
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	deps := Deps{
		Root:          "/work/demo",
		Backend:       "dbt",
		Engine:        newFakeEngine(fc.compile),
		Now:           testNow,
		DefaultBranch: "main",
		Copy:          func(s string) error { *copied = s; return nil },
		Open:          func(string, int) (Opening, error) { return Opening{}, nil },
		Info: &ProjectInfo{
			BackendFrom: "detected",
			Target:      "dev", TargetFrom: "$DBT_TARGET",
			Exe: "/home/me/go/bin/xf", Build: "devel 91ec614 2026-10-05",
			ToolBin: "/work/demo/.venv/bin/dbt", ToolFrom: "the project's virtualenv",
			Files: []InfoFile{{Path: "/home/me/.config/xf/config.json", Found: true}, {Path: "/work/demo/.xf/config.json"}},
			Settings: []InfoSetting{
				{Key: "dbtCompileWithHooks", Value: "false", From: "default: dbt v2 projects with hooks are parsed, not compiled"},
				{Key: "editor", Value: "nvim", From: "~/.config/xf/config.json"},
			},
			Warnings:      []string{`~/.config/xf/config.json:3: unknown key "edtor"`},
			EditorCommand: "nvim",
			Socket:        "/tmp/xf-0123456789abcdef-dbt-v10.sock",
			Log:           "/tmp/xf-0123456789abcdef-dbt-v10.log",
			ToolVersion:   func(context.Context) (string, error) { return "dbt 2.0.6", nil },
			Git: func(context.Context) (InfoGit, error) {
				return InfoGit{Branch: "feature/orders", Origin: "git@github.com:acme/shop.git"}, nil
			},
			ADC: func(context.Context) gcpauth.ADC {
				return gcpauth.ADC{
					Account: "me@work.example", Type: "authorized_user", QuotaProject: "old-sandbox",
					Source: "/home/me/.config/gcloud/application_default_credentials.json", SourceFrom: "gcloud's default file",
				}
			},
			Gcloud: func(context.Context) gcpauth.CLI {
				return gcpauth.CLI{Account: "me@home.example", Project: "my-proj"}
			},
		},
	}
	if edit != nil {
		edit(&deps)
	}
	m := New(deps)
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: w, Height: 60})
	return closePalette(t, m), copied
}

func TestProjectInfoOverlay(t *testing.T) {
	m, copied := infoTestModel(t, 120, nil)
	assertNotContains(t, screen(m), "project info")
	m = drive(t, m, nil, press(",")...)
	view := screen(m)
	assertContains(t, view,
		"project info",
		"root     /work/demo", "backend  dbt  detected", "target   dev  $DBT_TARGET",
		"xf   /home/me/go/bin/xf", "devel 91ec614 2026-10-05",
		"dbt  /work/demo/.venv/bin/dbt  the project's virtualenv", "dbt 2.0.6",
		"/home/me/.config/xf/config.json  read", "/work/demo/.xf/config.json       not found",
		"dbtCompileWithHooks  false  default: dbt v2", "editor               nvim   ~/.config/xf/config.json",
		"google cloud", "xf uses  me@work.example", "user login  for xf's own calls; dbt signs in as profiles.yml says",
		"/home/me/.config/gcloud/application_default_credentials.json  gcloud's default file", "quota    old-sandbox",
		"gcloud   me@home.example  project my-proj", "differs from the account xf uses",
		"branch         feature/orders", "origin         git@github.com:acme/shop.git", "compares with  main",
		"connected     no", "o opens with  nvim",
		"socket  /tmp/xf-0123456789abcdef-dbt-v10.sock", "log     /tmp/xf-0123456789abcdef-dbt-v10.log",
		`unknown key "edtor"`,
		"esc close", "y copy", "z zoom")

	m = drive(t, m, nil, press("y")...)
	for _, want := range []string{"root     /work/demo\n", "dbt 2.0.6\n", "branch         feature/orders\n", "xf uses  me@work.example\n"} {
		if !strings.Contains(*copied, want) {
			t.Errorf("copied text lacks %q:\n%s", want, *copied)
		}
	}

	m = drive(t, m, nil, press("z")...)
	assertContains(t, screen(m), "project info · zoom (esc)", "z unzoom")
	m = drive(t, m, nil, press("esc")...)
	if !m.info.open || m.info.zoomed {
		t.Fatalf("esc while zoomed: open %v, zoomed %v", m.info.open, m.info.zoomed)
	}
	m = drive(t, m, nil, press("esc")...)
	if m.info.open {
		t.Fatal("esc left the project info open")
	}

	// The palette has it, and so does the help.
	m = drive(t, m, nil, append(press("/"), typeText(">project info")...)...)
	assertContains(t, paletteView(m), "View: project info")
	m = drive(t, m, nil, press("enter")...)
	if !m.info.open {
		t.Fatal("the palette's command didn't open the project info")
	}
	m = drive(t, m, nil, press(",", "?")...)
	assertContains(t, screen(m), "project info")
}

func TestProjectInfoWrapsAndScrolls(t *testing.T) {
	m, copied := infoTestModel(t, 60, func(d *Deps) {
		d.Info.ToolBin = "/work/demo/a/very/deeply/nested/directory/of/virtual/environments/.venv/bin/dbt"
	})
	m = drive(t, m, tea.Batch(), tea.WindowSizeMsg{Width: 60, Height: 24})
	m = drive(t, m, nil, press(",")...)
	view := screen(m)
	// Wrapped under the value's column, not cut off.
	assertContains(t, view, "dbt  /work/demo/a/very/deeply/nested/", "       bin/dbt ", "       the project's virtualenv", "/ search", "↓")
	assertNotContains(t, view, "socket")
	m = drive(t, m, nil, press("G")...)
	assertContains(t, screen(m), "socket", "↑")
	m = drive(t, m, nil, press("y")...)
	if want := "dbt  /work/demo/a/very/deeply/nested/directory/of/virtual/environments/.venv/bin/dbt  the project's virtualenv\n"; !strings.Contains(*copied, want) {
		t.Errorf("copied text wraps the binary's path:\n%s", *copied)
	}
}

func TestProjectInfoFailuresShowInline(t *testing.T) {
	m, _ := infoTestModel(t, 120, func(d *Deps) {
		d.Info.ToolVersion = func(context.Context) (string, error) { return "", errors.New("exec format error\nmore") }
		d.Info.Git = func(context.Context) (InfoGit, error) { return InfoGit{}, errors.New("not a git repository") }
		d.Info.ADC = func(context.Context) gcpauth.ADC { return gcpauth.ADC{Error: "no Application Default Credentials"} }
		d.Info.Gcloud = func(context.Context) gcpauth.CLI { return gcpauth.CLI{Error: "gcloud not found on PATH"} }
	})
	m = drive(t, m, nil, press(",")...)
	view := screen(m)
	assertContains(t, view, "(unknown) exec format error", "branch         (not a git repository)",
		"xf uses  (not signed in)  no Application Default Credentials", gcpauth.LoginHint,
		"gcloud   (unknown)  gcloud not found on PATH")
	assertNotContains(t, view, "quota", "differs from")
	assertNotContains(t, view, "more", "origin")

	// An editor that says which file is active is connected.
	m = drive(t, m, nil, editorMsg{project.EditorEvent{Event: "BufEnter", File: "definitions/a.sqlx"}})
	assertContains(t, screen(m), "connected    yes  following", "active file  definitions/a.sqlx")
}

func TestNoProjectInfoWithoutIt(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = drive(t, m, nil, press(",")...)
	if m.info.open {
		t.Fatal(", opened project info that main didn't give")
	}
	m = drive(t, m, nil, press("?")...)
	assertNotContains(t, screen(m), "project info")
}

func TestProjectInfoSearch(t *testing.T) {
	m, copied := infoTestModel(t, 120, nil)
	m = drive(t, m, nil, press(",")...)
	assertContains(t, screen(m), "/ search")

	// The matches show as the query is typed, and its keys are text.
	m = drive(t, m, nil, append(press("/"), typeText("zq")...)...)
	if !m.info.open || !m.info.typing {
		t.Fatalf("typing q: open %v, typing %v", m.info.open, m.info.typing)
	}
	assertContains(t, screen(m), "project info · /zq · no match", "/zq")
	m = drive(t, m, nil, press("backspace", "backspace")...)
	m = drive(t, m, nil, typeText("GIT")...)
	// The origin's row, the section's title and the default branch's note.
	assertContains(t, screen(m), "project info · /GIT · 1 of 2")
	m = drive(t, m, nil, press("enter")...)
	if m.info.typing || m.info.cur != (infoAt{section: "git"}) {
		t.Fatalf("after enter: typing %v, current match %+v", m.info.typing, m.info.cur)
	}
	assertContains(t, screen(m), "esc clear search", "n/N next")

	// n and N go from row to row, around the ends.
	m = drive(t, m, nil, press("n")...)
	if want := (infoAt{section: "git", row: 2}); m.info.cur != want {
		t.Fatalf("n: current match %+v, want %+v", m.info.cur, want)
	}
	assertContains(t, screen(m), "/GIT · 2 of 2")
	m = drive(t, m, nil, press("n")...)
	assertContains(t, screen(m), "/GIT · 1 of 2")
	m = drive(t, m, nil, press("N")...)
	assertContains(t, screen(m), "/GIT · 2 of 2")

	// The copy has no marks of the search.
	m = drive(t, m, nil, press("y")...)
	if want := "origin         git@github.com:acme/shop.git\n"; !strings.Contains(*copied, want) {
		t.Errorf("copied text lacks %q:\n%s", want, *copied)
	}

	// Esc leaves the search, then the zoom, then the overlay.
	m = drive(t, m, nil, press("z", "esc")...)
	if q := m.info.query(); q != "" || !m.info.zoomed {
		t.Fatalf("first esc: query %q, zoomed %v", q, m.info.zoomed)
	}
	assertNotContains(t, screen(m), "/GIT")
	m = drive(t, m, nil, press("esc")...)
	if !m.info.open || m.info.zoomed {
		t.Fatalf("second esc: open %v, zoomed %v", m.info.open, m.info.zoomed)
	}
	m = drive(t, m, nil, press("esc")...)
	if m.info.open {
		t.Fatal("third esc left the project info open")
	}

	// Each open starts without a search.
	m = drive(t, m, nil, append(press(",", "/"), typeText("dbt")...)...)
	m = drive(t, m, nil, press("enter", "q", ",")...)
	if q := m.info.query(); !m.info.open || q != "" {
		t.Fatalf("reopened: open %v, query %q", m.info.open, q)
	}
	m = drive(t, m, nil, press(",", "?")...)
	assertContains(t, screen(m), ", project info", "next/prev match")
}

func TestProjectInfoSearchScrollsAndWraps(t *testing.T) {
	m, _ := infoTestModel(t, 60, func(d *Deps) {
		d.Info.ToolBin = "/work/demo/a/very/deeply/nested/directory/of/virtual/environments/.venv/bin/dbt"
	})
	m = drive(t, m, tea.Batch(), tea.WindowSizeMsg{Width: 60, Height: 24})
	m = drive(t, m, nil, press(",")...)
	assertNotContains(t, screen(m), "socket")

	// A match below the box scrolls into view; esc goes back.
	m = drive(t, m, nil, append(press("/"), typeText("socket")...)...)
	assertContains(t, screen(m), "socket  /tmp/", "1 of 1")
	if m.info.offset == 0 {
		t.Fatal("the search didn't scroll to its match")
	}
	m = drive(t, m, nil, press("esc")...)
	if m.info.offset != 0 || m.info.query() != "" {
		t.Fatalf("esc while typing: offset %d, query %q", m.info.offset, m.info.query())
	}

	// A match that is in view doesn't move it.
	m = drive(t, m, nil, append(press("/"), typeText("backend")...)...)
	if m.info.offset != 0 {
		t.Fatalf("a visible match scrolled the view to %d", m.info.offset)
	}
	m = drive(t, m, nil, press("esc")...)

	// A match the wrap cuts in two is one match, marked on both lines.
	q := "environments/.venv/bin"
	m = drive(t, m, nil, append(press("/"), typeText(q)...)...)
	assertContains(t, screen(m), "1 of 1")
	lines, _ := m.infoRender(m.infoLayout(m.geometry().bodyH).inner)
	marked := 0
	hit := m.styles.matchCurrent.Render("x")
	for _, l := range lines {
		if strings.Contains(l, hit[:strings.Index(hit, "x")]) {
			marked++
		}
	}
	if marked != 2 {
		t.Errorf("the match is marked on %d lines, want 2:\n%s", marked, strings.Join(lines, "\n"))
	}
}

func TestProjectInfoSearchKeepsItsRowWhenValuesArrive(t *testing.T) {
	m, _ := infoTestModel(t, 120, nil)
	m = drive(t, m, nil, press(",")...)
	m = drive(t, m, nil, append(press("/"), typeText("origin")...)...)
	m = drive(t, m, nil, press("enter")...)
	at, offset := m.info.cur, m.info.offset
	m.info.gcloud = gcpauth.CLI{Account: "origin@home.example"}
	assertContains(t, screen(m), "/origin · 2 of 2")
	if m.info.cur != at || m.info.offset != offset {
		t.Fatalf("a late value moved the search: %+v at %d, was %+v at %d", m.info.cur, m.info.offset, at, offset)
	}
}

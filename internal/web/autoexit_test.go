package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeTimers are timers a test fires itself.
type fakeTimers struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (c *fakeTimers) afterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

// last is the newest timer, nil for none.
func (c *fakeTimers) last() *fakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) == 0 {
		return nil
	}
	return c.timers[len(c.timers)-1]
}

// exitTest is an autoExit on fake timers, and what it did.
type exitTest struct {
	*autoExit
	clock *fakeTimers
	after time.Duration
	run   bool
	// term is the terminal command running, "" for none.
	term  string
	quits int
	lines []string
}

func newExitTest() *exitTest {
	e := &exitTest{clock: &fakeTimers{}, after: 45 * time.Second}
	e.autoExit = newAutoExit(func() time.Duration { return e.after }, func() string {
		if e.run {
			return "the run"
		}
		if e.term != "" {
			return "the terminal command"
		}
		return ""
	},
		func() { e.quits++ }, func(line string) { e.lines = append(e.lines, line) })
	e.autoExit.afterFunc = e.clock.afterFunc
	return e
}

// said is what was printed since the last call.
func (e *exitTest) said() string {
	out := strings.Join(e.lines, "\n")
	e.lines = nil
	return out
}

func TestAutoExitCountsDownWhenTheLastPageGoes(t *testing.T) {
	e := newExitTest()
	e.connect()
	e.connect()
	e.disconnect()
	if e.clock.last() != nil {
		t.Fatal("a page is still open, but it counts down")
	}
	e.disconnect()
	tm := e.clock.last()
	if tm == nil || tm.d != 45*time.Second {
		t.Fatalf("timer = %+v, want 45s", tm)
	}
	if got := e.said(); got != "all browser windows closed; shutting down in 45s (open the URL to resume)" {
		t.Errorf("said %q", got)
	}
	tm.f()
	if e.quits != 1 {
		t.Errorf("quits = %d, want 1", e.quits)
	}
	if got := e.said(); got != "no browser windows for 45s; stopped" {
		t.Errorf("said %q", got)
	}
	tm.f() // a second fire is nothing
	if e.quits != 1 {
		t.Errorf("quits = %d after a second fire", e.quits)
	}
}

func TestAutoExitReconnectCancels(t *testing.T) {
	e := newExitTest()
	e.connect()
	e.disconnect()
	tm := e.clock.last()
	e.said()
	e.connect()
	if !tm.stopped {
		t.Error("the timer wasn't stopped")
	}
	if got := e.said(); got != "browser window reconnected; shutdown canceled" {
		t.Errorf("said %q", got)
	}
	tm.f() // fired as it was stopped
	if e.quits != 0 {
		t.Error("a cancelled countdown quit")
	}
	// A connect with no countdown says nothing.
	e.connect()
	if got := e.said(); got != "" {
		t.Errorf("said %q", got)
	}
}

func TestAutoExitArmsOnTheFirstConnect(t *testing.T) {
	e := newExitTest()
	e.disconnect() // as if a stream had gone that never counted in
	if e.clock.last() != nil || e.said() != "" {
		t.Error("it counted down before any page connected")
	}
}

func TestAutoExitOff(t *testing.T) {
	e := newExitTest()
	e.after = 0
	e.connect()
	e.disconnect()
	if e.clock.last() != nil || e.said() != "" {
		t.Error("it counts down when off")
	}
	// The setting is read as each countdown starts; one going keeps its
	// length.
	e.after = 10 * time.Second
	e.connect()
	e.disconnect()
	tm := e.clock.last()
	if tm == nil || tm.d != 10*time.Second {
		t.Fatalf("timer = %+v, want 10s", tm)
	}
	e.after = time.Second
	tm.f()
	if got := e.said(); !strings.HasSuffix(got, "no browser windows for 10s; stopped") || e.quits != 1 {
		t.Errorf("said %q, quits %d", got, e.quits)
	}
}

func TestAutoExitWaitsForTheRun(t *testing.T) {
	e := newExitTest()
	e.run = true
	e.connect()
	e.disconnect()
	if e.clock.last() != nil {
		t.Fatal("it counts down during a run")
	}
	if got := e.said(); got != "all browser windows closed; waiting for the run to finish before shutting down" {
		t.Errorf("said %q", got)
	}
	e.run = false
	e.runEnded()
	tm := e.clock.last()
	if tm == nil || tm.d != 45*time.Second {
		t.Fatalf("timer = %+v, want the full 45s after the run", tm)
	}
	if got := e.said(); got != "the run finished; shutting down in 45s (open the URL to resume)" {
		t.Errorf("said %q", got)
	}
	tm.f()
	if e.quits != 1 {
		t.Errorf("quits = %d", e.quits)
	}
}

func TestAutoExitRunAtTheEndOfTheCountdown(t *testing.T) {
	e := newExitTest()
	e.connect()
	e.disconnect()
	e.run = true
	e.clock.last().f()
	if e.quits != 0 {
		t.Fatal("it quit during a run")
	}
	e.said()
	// A page coming back while it waits cancels it too.
	e.connect()
	if got := e.said(); got != "browser window reconnected; shutdown canceled" {
		t.Errorf("said %q", got)
	}
	e.run = false
	e.runEnded()
	if n := len(e.clock.timers); n != 1 {
		t.Errorf("%d timers: a run ending with a page open counted down", n)
	}
}

func TestAutoExitStop(t *testing.T) {
	e := newExitTest()
	e.connect()
	e.stop()
	e.disconnect()
	if e.clock.last() != nil || e.said() != "" {
		t.Error("pages going as xf web stops counted down")
	}
}

// TestAutoExitServer closes the only page of a server and waits for it to
// quit, on a real, short timer.
func TestAutoExitServer(t *testing.T) {
	quit := make(chan bool, 1)
	var mu sync.Mutex
	var lines []string
	s, err := New(Config{Token: "secret", AutoExit: func() time.Duration { return 50 * time.Millisecond },
		Quit: func() { quit <- true }, Log: func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() }})
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

	select {
	case <-quit:
		t.Fatal("it quit before any page connected")
	case <-time.After(150 * time.Millisecond):
	}
	page := streamEvents(t, c, ts.URL, "/api/events")
	waitFor(t, func() bool { s.exit.mu.Lock(); defer s.exit.mu.Unlock(); return s.exit.pages == 1 })
	page.close()
	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		t.Fatal("it didn't quit once the page went")
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(lines, "\n"); got != "all browser windows closed; shutting down in 0.05s (open the URL to resume)\nno browser windows for 0.05s; stopped" {
		t.Errorf("said %q", got)
	}
}

// TestAutoExitServerWaitsForARun closes the only page during a run: the
// server quits only once the run has ended and the countdown after it.
func TestAutoExitServerWaitsForARun(t *testing.T) {
	started := make(chan *fakeRun, 1)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx, opts: o}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done}, nil
	}
	quit := make(chan bool, 1)
	logged := make(chan string, 8)
	s, err := New(Config{Token: "secret", Project: &Project{Deps: frontend.Deps{Root: "/work/proj", Engine: newFakeEngine(testGraph()), StartRun: start}},
		AutoExit: func() time.Duration { return 50 * time.Millisecond }, Quit: func() { quit <- true }, Log: func(l string) { logged <- l }})
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
	waitFor(t, func() bool { return s.only().graph() != nil })

	page := streamEvents(t, c, ts.URL, "/api/events")
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"p.mart.orders"}}, nil)
	f := <-started
	page.close()
	next := func() string {
		t.Helper()
		select {
		case l := <-logged:
			return l
		case <-time.After(5 * time.Second):
			t.Fatal("nothing printed")
		}
		return ""
	}
	if l := next(); !strings.Contains(l, "waiting for the run to finish") {
		t.Fatalf("said %q", l)
	}
	select {
	case <-quit:
		t.Fatal("it quit during the run")
	case <-time.After(150 * time.Millisecond):
	}
	close(f.lines)
	f.done <- project.RunResult{Duration: time.Second}
	if l := next(); l != "the run finished; shutting down in 0.05s (open the URL to resume)" {
		t.Errorf("said %q", l)
	}
	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		t.Fatal("it didn't quit after the run")
	}
}

// A terminal command holds off auto-exit as a run does; the shell's
// prompt, told on every output while it is idle, ends the wait.
func TestAutoExitWaitsForTheTerminalCommand(t *testing.T) {
	e := newExitTest()
	e.term = "sleep"
	e.connect()
	e.disconnect()
	if e.clock.last() != nil {
		t.Fatal("it counts down while a terminal command runs")
	}
	if got := e.said(); got != "all browser windows closed; waiting for the terminal command to finish before shutting down" {
		t.Errorf("said %q", got)
	}
	e.runEnded() // output while the command still runs
	if e.clock.last() != nil {
		t.Fatal("it counts down before the command ended")
	}
	e.term = ""
	e.runEnded()
	tm := e.clock.last()
	if tm == nil || tm.d != 45*time.Second {
		t.Fatalf("timer = %+v, want 45s after the command", tm)
	}
	if got := e.said(); got != "the terminal command finished; shutting down in 45s (open the URL to resume)" {
		t.Errorf("said %q", got)
	}
}

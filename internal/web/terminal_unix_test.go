//go:build darwin || linux

package web

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
)

// waitForTerm reads ch until what it has sent holds want.
func waitForTerm(t *testing.T, ch chan []byte, seen *strings.Builder, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(seen.String(), want) {
		select {
		case b, ok := <-ch:
			if !ok {
				t.Fatalf("the terminal let go before %q; it showed %q", want, seen.String())
			}
			seen.Write(b)
		case <-deadline:
			t.Fatalf("no %q in %q", want, seen.String())
		}
	}
}

func TestTerminalRunsAndRestartsTheShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	term := newTerminal(dir, func() []string { return []string{"XF_TERM_TEST=yes"} }, nil)
	defer term.close()
	_, ch, err := term.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if err := term.resize(30, 100); err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	// The sum shows the shell ran it, not the echo of what was typed.
	if err := term.input([]byte("echo hi-$((1+2)) $(stty size) env-$XF_TERM_TEST\r")); err != nil {
		t.Fatal(err)
	}
	waitForTerm(t, ch, &seen, "hi-3 30 100 env-yes")

	if err := term.input([]byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	waitForTerm(t, ch, &seen, "process exited")
	// Keys other than enter don't start a shell.
	_ = term.input([]byte("x"))
	term.mu.Lock()
	running := term.running
	term.mu.Unlock()
	if running {
		t.Fatal("a key other than enter started a shell")
	}

	seen.Reset()
	if err := term.input([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	_ = term.input([]byte("echo again-$((2+2))\r"))
	waitForTerm(t, ch, &seen, "again-4")

	// A page that connects now is sent the screen so far.
	screen, ch2, err := term.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(screen), "again-4") {
		t.Fatalf("the replay %q misses the shell's output", screen)
	}
	term.close()
	select {
	case _, ok := <-ch2:
		for ok {
			_, ok = <-ch2
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the project kept the page")
	}
}

func TestTerminalRingKeepsTheLatestOutput(t *testing.T) {
	term := newTerminal(t.TempDir(), nil, nil)
	term.mu.Lock()
	term.sendLocked([]byte(strings.Repeat("a", termRingSize)))
	term.sendLocked([]byte("tail"))
	n, end := len(term.ring), string(term.ring[len(term.ring)-4:])
	term.mu.Unlock()
	if n != termRingSize || end != "tail" {
		t.Fatalf("ring is %d bytes ending %q, want %d ending \"tail\"", n, end, termRingSize)
	}
}

func TestTerminalSaysWhatCommandRuns(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	var idles atomic.Int32
	term := newTerminal(t.TempDir(), nil, func() { idles.Add(1) })
	defer term.close()
	_, ch, err := term.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	_ = term.input([]byte("echo ready\r"))
	waitForTerm(t, ch, &seen, "ready")
	if c := term.command(); c != "" {
		t.Fatalf("at the prompt the command is %q, want none", c)
	}
	_ = term.input([]byte("sleep 30\r"))
	waitUntil := time.Now().Add(5 * time.Second)
	for term.command() != "sleep" {
		if time.Now().After(waitUntil) {
			t.Fatalf("the command is %q, want sleep", term.command())
		}
		time.Sleep(20 * time.Millisecond)
	}
	before := idles.Load()
	_ = term.input([]byte("\x03echo b$((1))ack\r"))
	waitForTerm(t, ch, &seen, "b1ack")
	if c := term.command(); c != "" {
		t.Fatalf("after ctrl+c the command is %q, want none", c)
	}
	if idles.Load() == before {
		t.Fatal("the shell's prompt after ctrl+c didn't say it is idle")
	}
}

// A page is sent the screen so far first, even when there is none yet, so
// it can tell the replay from new output: the replay's queries (a TUI's
// mode or colour query) were answered when they were new, and answering
// them again would type the replies at the shell's prompt.
func TestTerminalStreamLeadsWithTheScreenEvenEmpty(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	s, ts, c := newTestProject(t, &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), Root: t.TempDir()}})
	defer s.only().terminal().close()
	ws := dialWS(t, c, ts.URL, "/api/term/stream", nil)
	if ws.conn == nil {
		t.Fatalf("the stream refused the page: %s", ws.res.Status)
	}
	op, p := ws.next(t)
	if op != wsBinary || len(p) != 0 {
		t.Fatalf("first frame is op %d with %q, want an empty binary one: the shell had shown nothing", op, p)
	}
}

// A project's terminal tab holds as many shells as the page asks for,
// each its own: keys typed in one reach only it, and closing one stops
// it and drops it from the list. Closing the last leaves a fresh one.
func TestTerminalTabsAreShellsOfTheirOwn(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	s, ts, c := newTestProject(t, &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph()), Root: t.TempDir()}})
	p := s.only()
	defer p.close()

	var v termsView
	getJSON(t, c, ts.URL+"/api/term/list", &v)
	if len(v.IDs) != 1 {
		t.Fatalf("a new project lists shells %v, want one", v.IDs)
	}
	first := v.IDs[0]
	postJSON(t, c, ts.URL+"/api/term/new", struct{}{}, &v)
	if len(v.IDs) != 2 || v.ID != v.IDs[1] || v.ID == first {
		t.Fatalf("new answered %+v, want a second shell, last", v)
	}
	second := v.ID

	_, ch1, err := p.terminalByID(first).subscribe()
	if err != nil {
		t.Fatal(err)
	}
	_, ch2, err := p.terminalByID(second).subscribe()
	if err != nil {
		t.Fatal(err)
	}
	postJSON(t, c, ts.URL+"/api/term/input", map[string]any{"term": second, "data": "echo two-$((1+1))\r"}, nil)
	postJSON(t, c, ts.URL+"/api/term/input", map[string]any{"term": first, "data": "echo one-$((0+1))\r"}, nil)
	var seen1, seen2 strings.Builder
	waitForTerm(t, ch2, &seen2, "two-2")
	waitForTerm(t, ch1, &seen1, "one-1")
	if strings.Contains(seen1.String(), "two-2") || strings.Contains(seen2.String(), "one-1") {
		t.Fatalf("a shell showed the other's output:\nfirst %q\nsecond %q", seen1.String(), seen2.String())
	}

	postJSON(t, c, ts.URL+"/api/term/close", map[string]int{"term": second}, &v)
	if len(v.IDs) != 1 || v.IDs[0] != first {
		t.Fatalf("after closing the second, shells %v, want [%d]", v.IDs, first)
	}
	if p.terminalByID(second) != nil {
		t.Fatal("the closed shell is still found")
	}
	for range ch2 { // closing it lets its pages go
	}
	res, err := c.Post(ts.URL+"/api/term/input", "application/json", strings.NewReader(`{"term":`+strconv.Itoa(second)+`,"data":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("keys for a closed shell: %s, want 404", res.Status)
	}

	postJSON(t, c, ts.URL+"/api/term/close", map[string]int{"term": first}, &v)
	if len(v.IDs) != 1 || v.IDs[0] == first || v.IDs[0] == second {
		t.Fatalf("after closing the last, shells %v, want a fresh one", v.IDs)
	}
}

// A shell's tab is named after the command running in it, else after the
// shell, and named is told each change: a command that prints nothing
// renames it too.
func TestTerminalNamesItsTabAfterTheCommand(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	term := newTerminal(t.TempDir(), nil, nil)
	var renames atomic.Int32
	term.named = func() { renames.Add(1) }
	defer term.close()
	if n := term.title(); n != "" {
		t.Fatalf("before it starts the tab is %q, want none", n)
	}
	_, ch, err := term.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range ch {
		}
	}()
	waitForName := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for term.title() != want {
			if time.Now().After(deadline) {
				t.Fatalf("the tab is %q, want %q", term.title(), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// macOS's /bin/sh starts bash: the shell's name is what it settles on.
	deadline := time.Now().Add(10 * time.Second)
	for term.title() == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	shell := term.title()
	if shell == "" || shell == "sleep" {
		t.Fatalf("at its prompt the tab is %q, want the shell's name", shell)
	}
	_ = term.input([]byte("sleep 30\r"))
	waitForName("sleep")
	before := renames.Load()
	_ = term.input([]byte("\x03"))
	waitForName(shell)
	if renames.Load() == before {
		t.Fatal("named wasn't told the command ended")
	}
}

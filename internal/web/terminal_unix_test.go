//go:build darwin || linux

package web

import (
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

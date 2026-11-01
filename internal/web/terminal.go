package web

// The terminal tab: one shell per open project, on a
// pseudo-terminal the server owns, so it outlives a reload. Its output
// reaches pages over a WebSocket of its own (binary frames, not the event
// stream's JSON), led by the last termRingSize bytes so a page that
// connects late sees the screen as it is. Keys and sizes come as POSTs.

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// termRingSize is how much of the shell's latest output a page that
// connects is sent first.
const termRingSize = 128 << 10

// termExited is written to the terminal when its shell ends.
const termExited = "\r\n\x1b[2m[process exited · press enter to restart]\x1b[0m\r\n"

// terminal is a project's shell. mu guards every field but the first.
type terminal struct {
	dir string
	// env is added to the shell's environment (Config.TermEnv); nil adds
	// nothing.
	env func() []string
	// idle is told, with no lock held, each time the shell shows output
	// while no command runs (its prompt, once one has ended), and when it
	// exits; nil tells no one.
	idle func()

	mu      sync.Mutex
	pty     *os.File
	cmd     *exec.Cmd
	running bool
	started bool // once a shell has started, a new one waits for enter
	closed  bool
	ring    []byte
	subs    map[chan []byte]struct{}
	rows    uint16
	cols    uint16
}

func newTerminal(dir string, env func() []string, idle func()) *terminal {
	return &terminal{dir: dir, env: env, idle: idle, subs: map[chan []byte]struct{}{}, rows: 24, cols: 80}
}

var errTermClosed = errors.New("the project is closed")

// startLocked starts a shell unless one is running.
func (t *terminal) startLocked() error {
	if t.closed {
		return errTermClosed
	}
	if t.running {
		return nil
	}
	var env []string
	if t.env != nil {
		env = t.env()
	}
	m, cmd, err := startShell(t.dir, env, t.rows, t.cols)
	if err != nil {
		return err
	}
	t.pty, t.cmd, t.running, t.started = m, cmd, true, true
	go t.pump(m, cmd)
	return nil
}

// pump sends the shell's output on until it ends.
func (t *terminal) pump(m *os.File, cmd *exec.Cmd) {
	buf := make([]byte, 32<<10)
	for {
		n, err := m.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.sendLocked(append([]byte(nil), buf[:n]...))
			t.mu.Unlock()
			if t.idle != nil && t.command() == "" {
				t.idle()
			}
		}
		if err != nil {
			break // EIO once the shell and all it started are gone
		}
	}
	_ = cmd.Wait()
	_ = m.Close()
	t.mu.Lock()
	if t.cmd == cmd {
		t.running, t.pty, t.cmd = false, nil, nil
		if !t.closed {
			t.sendLocked([]byte(termExited))
		}
	}
	t.mu.Unlock()
	if t.idle != nil {
		t.idle()
	}
}

// command is the name of the command running in the shell, "" while the
// shell waits at its prompt or isn't running. One whose name can't be
// read is "a command".
func (t *terminal) command() string {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return ""
	}
	m, shell := t.pty, t.cmd.Process.Pid
	t.mu.Unlock()
	pgrp, ok := foreground(m)
	if !ok || pgrp == shell || pgrp <= 0 {
		return ""
	}
	return cmp.Or(procName(pgrp), "a command")
}

// sendLocked keeps b in the ring and passes it to each page. A page too
// slow to keep up is let go: it reconnects and starts again from the ring.
func (t *terminal) sendLocked(b []byte) {
	t.ring = append(t.ring, b...)
	if over := len(t.ring) - termRingSize; over > 0 {
		t.ring = append(t.ring[:0], t.ring[over:]...)
	}
	for ch := range t.subs {
		select {
		case ch <- b:
		default:
			delete(t.subs, ch)
			close(ch)
		}
	}
}

// subscribe starts the first shell, and answers what the terminal shows
// so far and a channel of what follows, closed when the page is let go.
func (t *terminal) subscribe() ([]byte, chan []byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		if err := t.startLocked(); err != nil {
			return nil, nil, err
		}
	} else if t.closed {
		return nil, nil, errTermClosed
	}
	ch := make(chan []byte, 256)
	t.subs[ch] = struct{}{}
	return append([]byte(nil), t.ring...), ch, nil
}

func (t *terminal) unsubscribe(ch chan []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.subs[ch]; ok {
		delete(t.subs, ch)
		close(ch)
	}
}

// input types b into the shell. Once the shell has ended, enter starts a
// new one and other keys are dropped.
func (t *terminal) input(b []byte) error {
	t.mu.Lock()
	if !t.running {
		defer t.mu.Unlock()
		for _, c := range b {
			if c == '\r' {
				t.ring = nil
				t.sendLocked([]byte("\x1bc")) // a fresh screen for the new shell
				return t.startLocked()
			}
		}
		return nil
	}
	m := t.pty
	t.mu.Unlock()
	_, err := m.Write(b)
	return err
}

// run types line into the shell, then enter, starting a shell when none
// runs. It refuses while a command runs there, which would get the line.
func (t *terminal) run(line string) error {
	if c := t.command(); c != "" {
		return fmt.Errorf("the terminal is running %s: quit it first", c)
	}
	t.mu.Lock()
	if !t.running {
		if t.started {
			t.ring = nil
			t.sendLocked([]byte("\x1bc")) // a fresh screen for the new shell
		}
		if err := t.startLocked(); err != nil {
			t.mu.Unlock()
			return err
		}
	}
	m := t.pty
	t.mu.Unlock()
	_, err := m.Write([]byte(line + "\r"))
	return err
}

// resize sets the terminal's size, kept for the next shell too.
func (t *terminal) resize(rows, cols uint16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows, t.cols = rows, cols
	if !t.running {
		return nil
	}
	return setPTYSize(t.pty, rows, cols)
}

// close stops the shell and lets every page go, for good.
func (t *terminal) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.running {
		stopShell(t.cmd)
	}
	for ch := range t.subs {
		delete(t.subs, ch)
		close(ch)
	}
}

// terminal is p's, made the first time it is asked for.
func (p *projectState) terminal() *terminal {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.term == nil {
		idle := p.runEnded
		p.term = newTerminal(p.deps.Root, p.termEnv, func() {
			// Back at the prompt, an install may have just finished.
			p.recompileOnceFound()
			p.recompileAfterFix()
			if idle != nil {
				idle()
			}
		})
	}
	return p.term
}

// termCommand is the command running in p's terminal, "" for none.
func (p *projectState) termCommand() string {
	p.mu.Lock()
	t := p.term
	p.mu.Unlock()
	if t == nil {
		return ""
	}
	return t.command()
}

// termState is GET /api/term/state: the command running in the terminal,
// which closing the project would stop.
func (p *projectState) termState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, struct {
		Command string `json:"command"`
	}{p.termCommand()})
}

// termStream is GET /api/term/stream: the terminal's output, over a
// WebSocket. A WebSocket can't carry the project header, so the project
// comes in the query, as the event stream's does.
func (s *Server) termStream(w http.ResponseWriter, r *http.Request) {
	if !termSupported {
		http.Error(w, "the terminal tab needs macOS or Linux", http.StatusNotFound)
		return
	}
	var p *projectState
	if key := r.URL.Query().Get("project"); key != "" {
		s.mu.Lock()
		if op := s.openByKeyLocked(key); op != nil {
			p = op.p
		}
		s.mu.Unlock()
		if p == nil {
			http.Error(w, errNotOpen.Error(), http.StatusConflict)
			return
		}
	} else if p = s.project(w, r); p == nil {
		return
	}
	t := p.terminal()
	screen, ch, err := t.subscribe()
	if err != nil {
		writeError(w, err)
		return
	}
	defer t.unsubscribe(ch)
	ws := upgradeWS(w, r)
	if ws == nil {
		return
	}
	defer ws.close(1000)
	// The screen so far goes first, even empty: the page tells it from
	// new output by being first, and doesn't answer the queries in it.
	if ws.write(wsBinary, screen) != nil {
		return
	}
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		select {
		case <-ws.done:
			return
		case <-tick.C:
			if ws.write(wsPing, nil) != nil {
				return
			}
		case b, ok := <-ch:
			if !ok || ws.write(wsBinary, b) != nil {
				return
			}
		}
	}
}

// termInput is POST /api/term/input: keys typed in the terminal tab.
func (p *projectState) termInput(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Data string `json:"data"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := p.terminal().input([]byte(req.Data)); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, okView{OK: true})
}

// cliMissing says the project opened without its CLI and it still can't
// be found.
func (p *projectState) cliMissing() bool {
	p.mu.Lock()
	in, found := p.install, p.cliFound
	p.mu.Unlock()
	return in != nil && !found && !in.Found()
}

// recompileOnceFound compiles the project again the first time the CLI it
// opened without can be found, while its compile still fails.
func (p *projectState) recompileOnceFound() {
	p.mu.Lock()
	in, waiting := p.install, p.install != nil && !p.cliFound && p.st.Err != nil
	p.mu.Unlock()
	if !waiting || !in.Found() {
		return
	}
	p.mu.Lock()
	again := !p.cliFound
	p.cliFound = true
	p.mu.Unlock()
	if again {
		p.deps.Engine.Recompile(in.Name + " is installed")
	}
}

// termResize is POST /api/term/resize: the terminal tab's rows and columns.
func (p *projectState) termResize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := p.terminal().resize(req.Rows, req.Cols); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, okView{OK: true})
}

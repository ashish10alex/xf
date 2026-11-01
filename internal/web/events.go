package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// event is one of the event stream's messages: its name and its data as
// JSON.
type event struct {
	name string
	data []byte
}

// hub sends events to every open page.
type hub struct {
	mu      sync.Mutex
	clients map[chan event]bool
	closed  bool
	done    chan struct{}
	// watch, set before any send, is told each event's name once it is
	// sent.
	watch func(name string)
}

func newHub() *hub { return &hub{clients: map[chan event]bool{}, done: make(chan struct{})} }

// clientBuffer is how many events a page may fall behind by before it is
// sent no more until it catches up; it then reconnects and starts again.
const clientBuffer = 512

func (h *hub) subscribe() (chan event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, false
	}
	ch := make(chan event, clientBuffer)
	h.clients[ch] = true
	return ch, true
}

func (h *hub) unsubscribe(ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, ch)
}

// send sends an event to every page, dropping a page that is too far
// behind (it reconnects and asks for everything again), and tells watch.
func (h *hub) send(name string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte("null")
	}
	if h.watch != nil {
		defer h.watch(name)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- event{name, data}:
		default:
			delete(h.clients, ch)
			close(ch)
		}
	}
}

// count is how many pages listen.
func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.done)
	for ch := range h.clients {
		close(ch)
	}
	h.clients = nil
}

// keepAlive is how often an idle stream's page is pinged, so proxies keep
// it open and a page that went without closing it is found out.
const keepAlive = 20 * time.Second

// events streams the server's events, and those of the project the page
// names (?project=), to a page over a WebSocket (see websocket.go) until it
// goes, the server stops or the project closes (the last event, "closed",
// sends the page elsewhere). Each message is {"event": name, "data": …};
// the first is "hello", after which the page asks for what it shows. The
// streams are what keep xf web going.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	var op *openProject
	if key := r.URL.Query().Get("project"); key != "" {
		var err error
		if op, err = s.ensure(key); err != nil {
			writeError(w, err)
			return
		}
	}
	ch, ok := s.hub.subscribe()
	if !ok {
		http.Error(w, "xf web is stopping", http.StatusServiceUnavailable)
		return
	}
	defer s.hub.unsubscribe(ch)
	// pch is the project's events; nil, for a page on the projects,
	// never sends.
	var pch chan event
	if op != nil {
		if pch, ok = op.p.hub.subscribe(); !ok {
			http.Error(w, errNotOpen.Error(), http.StatusConflict)
			return
		}
		defer op.p.hub.unsubscribe(pch)
	}
	ws := upgradeWS(w, r)
	if ws == nil {
		return
	}
	defer ws.close(1000)
	if op != nil {
		s.touch(op)
	}
	s.exit.connect()
	defer s.exit.disconnect()
	tab := r.URL.Query().Get("tab")
	s.tabConnected(tab)
	defer s.tabGone(tab)
	send := func(ev event) bool {
		name, _ := json.Marshal(ev.name)
		msg := make([]byte, 0, len(ev.data)+len(name)+20)
		msg = append(append(append(append(msg, `{"event":`...), name...), `,"data":`...), ev.data...)
		return ws.write(wsText, append(msg, '}')) == nil
	}
	if !send(event{"hello", []byte("{}")}) {
		return
	}
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		select {
		case <-ws.done:
			return // the page went
		case <-tick.C:
			if ws.write(wsPing, nil) != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok || !send(ev) {
				return
			}
		case ev, ok := <-pch:
			if !ok || !send(ev) {
				return
			}
		}
	}
}

// autoExit quits xf web once every page has been gone for a while, as
// ctrl+c does. It counts the event streams, one per page (each is one of
// the hub's clients). The countdown starts when the last page goes, and
// not before a page has connected once (with --no-open, or a browser
// that failed to open, nothing counts down). A page that connects
// cancels it: a reload is back within a second, and a sleeping machine
// doesn't run timers. A run holds it off: when the last page goes during
// a run, or the countdown ends in one, it waits for the run to end, then
// counts down again from the start.
type autoExit struct {
	// after is the countdown's length, read as each starts; 0 is off.
	after func() time.Duration
	// busy is what must end before xf web stops: "the run", say, or ""
	// for nothing.
	busy func() string
	quit func()
	log  func(line string)
	// afterFunc is time.AfterFunc's, which tests replace.
	afterFunc func(d time.Duration, f func()) (stop func() bool)

	mu    sync.Mutex
	pages int
	// armed: a page has connected.
	armed bool
	// gen tells the countdown going from one that was cancelled.
	gen       int
	stopTimer func() bool
	length    time.Duration
	// waiting: every page has gone, and waitedFor (a run, say) must end
	// first.
	waiting   bool
	waitedFor string
	// stopped: xf web is stopping, so pages going is no news.
	stopped bool
}

func newAutoExit(after func() time.Duration, busy func() string, quit func(), log func(string)) *autoExit {
	if after == nil || quit == nil {
		after = func() time.Duration { return 0 }
	}
	if log == nil {
		log = func(string) {}
	}
	return &autoExit{after: after, busy: busy, quit: quit, log: log,
		afterFunc: func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }}
}

// connect counts a page in, cancelling a countdown.
func (a *autoExit) connect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pages++
	a.armed = true
	if a.stopped || a.stopTimer == nil && !a.waiting {
		return
	}
	a.cancelLocked()
	a.log("browser window reconnected; shutdown canceled")
}

// disconnect counts a page out; the last starts the countdown.
func (a *autoExit) disconnect() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pages--
	if a.pages > 0 || !a.armed || a.stopped {
		return
	}
	a.startLocked("all browser windows closed")
}

// runEnded starts the countdown that waited for the run, or for a
// terminal command: the terminal says so whenever its shell is back at
// its prompt.
func (a *autoExit) runEnded() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.waiting || a.pages > 0 || a.stopped || a.busy() != "" {
		return
	}
	what := a.waitedFor
	a.waiting = false
	a.startLocked(what + " finished")
}

// stop ends any countdown for good: xf web is stopping.
func (a *autoExit) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancelLocked()
	a.stopped = true
}

func (a *autoExit) cancelLocked() {
	a.gen++
	if a.stopTimer != nil {
		a.stopTimer()
		a.stopTimer = nil
	}
	a.waiting = false
}

// startLocked counts down, or with a run going waits for it; why is what
// happened, for the line it prints.
func (a *autoExit) startLocked(why string) {
	d := a.after()
	if d <= 0 {
		return
	}
	if what := a.busy(); what != "" {
		a.waiting, a.waitedFor = true, what
		a.log(why + "; waiting for " + what + " to finish before shutting down")
		return
	}
	a.cancelLocked()
	gen := a.gen
	a.length = d
	a.stopTimer = a.afterFunc(d, func() { a.fire(gen) })
	a.log(why + "; shutting down in " + seconds(d) + " (open the URL to resume)")
}

// fire ends the countdown gen, unless it was cancelled or a run started.
func (a *autoExit) fire(gen int) {
	a.mu.Lock()
	if gen != a.gen || a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopTimer = nil
	if what := a.busy(); what != "" {
		a.waiting, a.waitedFor = true, what
		a.log("all browser windows closed; waiting for " + what + " to finish before shutting down")
		a.mu.Unlock()
		return
	}
	a.stopped = true
	a.log("no browser windows for " + seconds(a.length) + "; stopped")
	a.mu.Unlock()
	a.quit()
}

// seconds is d as the terminal shows it: 45s.
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/frontend"
)

// watchWorkflow follows a workflow invocation xf did not start: a scheduled
// one, a colleague's, one from the console. It takes the run's place, so
// its log, its jobs table (what each action's BigQuery job cost) and the
// kept run in the history are those of any API run; if the invocation is
// still going, they update as it goes. It is kept in the history as
// external.
//
// The invocation is looked up before the answer, so an ID that is not found
// (or a repository or location that is wrong) comes back to the prompt
// that asked, and takes nothing of the run's place.
func (p *projectState) watchWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input string `json:"input"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	d := p.deps
	if d.WatchWorkflow == nil {
		writeJSON(w, okView{Message: "watching a workflow is not available"})
		return
	}
	ref, err := dataformapi.ParseInvocation(req.Input)
	if err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	p.mu.Lock()
	if p.run.running || p.run.looking {
		p.mu.Unlock()
		writeJSON(w, okView{Message: "a run is already in progress (ctrl+c cancels it)"})
		return
	}
	p.run.looking = true
	// What the project knows of where its workflows are; a pasted URL says
	// the rest, and covers a project that has not compiled yet.
	var base dataformapi.Repository
	if g := p.st.Graph; g != nil {
		base = frontend.APIRunOptions(g, false, false, false).Repo
	}
	p.mu.Unlock()

	// Not the request's context: the watch outlives it.
	ctx, cancel := context.WithCancel(context.Background())
	run, _, err := d.WatchWorkflow(ctx, ref, base)

	p.mu.Lock()
	p.run.looking = false
	switch {
	case err != nil:
		p.mu.Unlock()
		cancel()
		msg := err.Error()
		if errors.Is(err, dataformapi.ErrAuth) {
			msg += ": to log in, " + dataformapi.AuthHint
		}
		writeJSON(w, okView{Message: msg})
		return
	case p.run.running: // a run started meanwhile
		p.mu.Unlock()
		if run.Kill != nil {
			run.Kill()
		}
		cancel()
		writeJSON(w, okView{Message: "a run is already in progress (ctrl+c cancels it)"})
		return
	}
	p.run.seq++
	seq := p.run.seq
	p.run.running, p.run.cancelling, p.run.api, p.run.watching, p.run.ran = true, false, true, true, true
	p.run.label = "workflow " + shortWorkflowID(ref.ID)
	p.run.short, p.run.title = p.run.label, "watching…"
	p.run.lines, p.run.sent, p.run.run, p.run.cancel, p.run.report = []logLine{}, 0, nil, cancel, nil
	p.run.command = ""
	v := p.runViewLocked(true)
	p.mu.Unlock()
	p.hub.send("run", v)
	writeJSON(w, okView{OK: true})
	go p.follow(seq, run)
}

// shortWorkflowID is a workflow's ID short enough for the status line: the
// last group of the UUID is the part that tells two apart.
func shortWorkflowID(id string) string {
	if len(id) <= 24 {
		return id
	}
	return "…" + id[len(id)-12:]
}

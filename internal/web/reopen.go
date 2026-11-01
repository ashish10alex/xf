package web

import (
	"cmp"
	"errors"
	"net/http"
	"time"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

// A reopen applies the settings a project opens with (its names, dbt's
// target, the CLI). The project opens again beside the one on show, which
// stays open and drawn, and its pages reload only once the new one has
// compiled: they land on its graph, with the same action selected, rather
// than on empty panes while it compiles (ADR 0022).

// reopenWait is how long a reopen waits for the new project's first
// compile before it goes over anyway.
var reopenWait = 90 * time.Second

// reopen opens the page's project again, so settings that apply when it
// opens do; the selection comes with its session, and every tab on it
// reloads. A local run would be cancelled, so it waits for that to end.
func (s *Server) reopen(w http.ResponseWriter, r *http.Request) {
	op := s.projectOf(r)
	if op == nil {
		http.Error(w, "no project is open", http.StatusConflict)
		return
	}
	p := op.p
	if s.cfg.Open == nil || p.standalone {
		writeError(w, errors.New("this project can't be reopened from the page: restart xf web"))
		return
	}
	if p.localRunning() {
		writeError(w, errReopenRun)
		return
	}
	if !p.reopening.CompareAndSwap(false, true) {
		writeJSON(w, okView{OK: true}) // another tab's reopen is on its way
		return
	}
	p.hub.send("reopening", true) // every page on it says so
	failed := func(err error) {
		p.reopening.Store(false)
		p.hub.send("reopening", false)
		writeError(w, err)
	}
	opened, err := s.cfg.Open(session.Project{Root: p.deps.Root, Backend: cmp.Or(p.deps.Backend, "dataform")})
	if err != nil {
		failed(err)
		return
	}
	now := firstGraph(opened.Deps.Engine, reopenWait)

	s.opening.Lock()
	defer s.opening.Unlock()
	s.mu.Lock()
	current := s.open[p.deps.Root] == op
	s.mu.Unlock()
	if !current || p.localRunning() {
		if opened.Close != nil {
			opened.Close()
		}
		if !current { // closed meanwhile
			writeJSON(w, okView{OK: true})
			return
		}
		failed(errReopenRun)
		return
	}
	p.mu.Lock()
	moved := movedActions(p.st.Graph, now)
	opened.Deps.Resume = carrySession(p.sess, moved)
	p.mu.Unlock()
	// Its pages open the new one. Each keeps its own view (workspace.js),
	// which they save under the new IDs: one under an old ID would draw
	// nothing selected first, then the action once it was found again.
	p.hub.send("reload", reloadView{Moved: moved})
	s.drop(op)
	s.adopt(opened)
	writeJSON(w, okView{OK: true})
}

var errReopenRun = errors.New("a run is going: reopen once it ends, or cancel it (ctrl+c)")

// localRunning reports whether a run of p from this machine is going; an
// API run carries on in GCP whatever the page does.
func (p *projectState) localRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.run.running && !p.run.api
}

// firstGraph waits, up to d, for e's first compile (a saved one counts),
// and is its graph then; nil when e can't say, which waits for nothing.
func firstGraph(e frontend.Engine, d time.Duration) *project.Graph {
	c, ok := e.(interface {
		FirstCompile() <-chan struct{}
		State() engine.State
	})
	if !ok {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-c.FirstCompile():
	case <-t.C:
	}
	return c.State().Graph
}

// reloadView is the reload event: the actions whose IDs a reopen changed.
type reloadView struct {
	Moved map[project.ActionID]project.ActionID `json:"moved"`
}

// carrySession is sess with the actions moved names: their IDs as the
// project now compiles.
func carrySession(sess session.Session, moved map[project.ActionID]project.ActionID) session.Session {
	sess.Selected = cmp.Or(moved[sess.Selected], sess.Selected)
	recent := make([]project.ActionID, 0, len(sess.Recent))
	seen := map[project.ActionID]bool{}
	for _, id := range sess.Recent {
		if id = cmp.Or(moved[id], id); !seen[id] {
			seen[id] = true
			recent = append(recent, id)
		}
	}
	sess.Recent = recent
	return sess
}

// movedActions maps each action of old whose ID now has changed to its
// new one: the names it builds may have changed, and its ID with them, so
// it is found again by its file and the name a prefix doesn't change. An
// action now has under the same ID, or doesn't have, isn't in it.
func movedActions(old, now *project.Graph) map[project.ActionID]project.ActionID {
	moved := map[project.ActionID]project.ActionID{}
	if old == nil || now == nil {
		return moved
	}
	type key struct{ file, name string }
	byName := map[key]project.ActionID{}
	for _, a := range now.Actions {
		if !a.Kind.External() {
			byName[key{a.FileName, canonicalName(a)}] = a.ID
		}
	}
	for id, a := range old.Actions {
		if now.Actions[id] != nil {
			continue
		}
		if n := byName[key{a.FileName, canonicalName(a)}]; n != "" {
			moved[id] = n
		}
	}
	return moved
}

// canonicalName is the name a's file gives it, before any prefix.
func canonicalName(a *project.Action) string {
	return cmp.Or(a.CanonicalTarget.Name, a.Target.Name)
}

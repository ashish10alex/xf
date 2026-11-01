package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/session"
)

// The status line's branch and the B switcher, as in the TUI (see
// ui/branchview.go). The server reads the branch and sends it to every
// page as a "branch" event when HEAD or another watched git file changes
// and after a switch or fetch; a page also asks for it at start, when a
// compile starts and when it gets focus back. Unsaved edits in the source
// pane are the page's to know, so the page refuses a switch for them; a
// local run, which every page shares, is refused here.

// branchView is the status line's branch: its parts in their tones, none
// outside a repository.
type branchView struct {
	Parts []frontend.GitPart `json:"parts"`
}

// readBranch reads the checkout for the status line.
func (p *projectState) readBranch(ctx context.Context) branchView {
	if p.deps.Git == nil {
		return branchView{Parts: []frontend.GitPart{}}
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	g, err := p.deps.Git(ctx)
	return branchView{Parts: nonNil(frontend.BranchParts(g, err, true))}
}

func (p *projectState) branch(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, p.readBranch(r.Context()))
}

// sendBranch tells every page the branch, as it is now. A change asks
// the merge request's host again.
func (p *projectState) sendBranch() {
	v := p.readBranch(context.Background())
	p.hub.send("branch", v)
	p.forgeBranchSeen(branchKey(v))
}

// branchKey is what forgeBranchSeen compares to see the branch change.
func branchKey(v branchView) string {
	b, _ := json.Marshal(v.Parts)
	return string(b)
}

// watchHead sends the branch whenever a watched git file changes, until
// ctx is done. It first notes the branch the project opened on, so the
// first switch is seen as a change and asks the host again.
func (p *projectState) watchHead(ctx context.Context) {
	p.forgeBranchSeen(branchKey(p.readBranch(ctx)))
	if p.deps.Branches == nil {
		return
	}
	ch, err := p.deps.Branches.Watch(ctx, 150*time.Millisecond)
	if err != nil {
		return // not a repository, or no watch: focus still reads it
	}
	for range ch {
		p.sendBranch()
	}
}

// branchRow is a branch as the switcher lists it.
type branchRow struct {
	Name    string `json:"name"`
	Remote  bool   `json:"remote,omitempty"`
	Current bool   `json:"current,omitempty"`
	Age     string `json:"age"`
	// Worktree is the other checkout the branch is in, as the picker
	// shows paths: shift+enter opens it there (ADR 0032).
	Worktree string `json:"worktree,omitempty"`
}

// branchesView is the switcher's list: the branches ?q matches, best
// first, of Total; Note says when a switch changes more than the project.
type branchesView struct {
	Branches []branchRow `json:"branches"`
	Total    int         `json:"total"`
	Note     string      `json:"note,omitempty"`
	Detached string      `json:"detached,omitempty"`
	// NotGit says the project is in no git repository.
	NotGit bool   `json:"notGit,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (p *projectState) branches(w http.ResponseWriter, r *http.Request) {
	if p.deps.Branches == nil {
		writeJSON(w, branchesView{Branches: []branchRow{}, Error: "switching branch is not available"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	l, err := p.deps.Branches.List(ctx)
	switch {
	case errors.Is(err, gitbranch.ErrNotGit):
		writeJSON(w, branchesView{Branches: []branchRow{}, NotGit: true})
		return
	case err != nil:
		writeJSON(w, branchesView{Branches: []branchRow{}, Error: err.Error()})
		return
	}
	now := p.deps.Now()
	v := branchesView{Branches: []branchRow{}, Total: len(l.Branches), Note: frontend.BranchRootNote(p.deps.Root, l.Root), Detached: l.Detached}
	for _, b := range frontend.FindBranches(l.Branches, r.URL.Query().Get("q")) {
		v.Branches = append(v.Branches, branchRow{Name: b.Name, Remote: b.Remote, Current: b.Current, Age: gitbranch.Age(now, b.Date), Worktree: branchWorktree(b)})
	}
	writeJSON(w, v)
}

// branchTimeout bounds a switch; fetchTimeout a fetch, which goes to the
// network.
const (
	branchTimeout = 30 * time.Second
	fetchTimeout  = 2 * time.Minute
)

func (p *projectState) switchBranch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if p.deps.Branches == nil {
		writeJSON(w, okView{Message: "switching branch is not available"})
		return
	}
	p.mu.Lock()
	local := p.run.running && !p.run.api
	p.mu.Unlock()
	if local {
		writeJSON(w, okView{Message: frontend.BranchRunBlocker})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), branchTimeout)
	defer cancel()
	if err := p.deps.Branches.Switch(ctx, req.Name); err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	p.mu.Lock()
	watchOff := p.st.WatchErr != nil
	p.mu.Unlock()
	if watchOff { // nothing else recompiles
		p.deps.Engine.Recompile("switched to " + req.Name)
	}
	p.sendBranch()
	writeJSON(w, okView{OK: true, Message: frontend.BranchSwitched(req.Name)})
}

func (p *projectState) fetchBranches(w http.ResponseWriter, r *http.Request) {
	if p.deps.Branches == nil {
		writeJSON(w, okView{Message: "switching branch is not available"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fetchTimeout)
	defer cancel()
	if err := p.deps.Branches.Fetch(ctx); err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	p.sendBranch()
	p.forgeCheck()
	writeJSON(w, okView{OK: true})
}

// pickerBranchTimeout bounds the read of one project's branch.
const pickerBranchTimeout = 3 * time.Second

// pickerBranches is the branch of each remembered project, by root,
// read at once; "" outside a repository.
func (s *Server) pickerBranches(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	if s.cfg.Branch == nil {
		writeJSON(w, out)
		return
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	seen := map[string]bool{}
	for _, pr := range s.projects() {
		if seen[pr.Root] {
			continue
		}
		seen[pr.Root] = true
		wg.Add(1)
		go func(pr session.Project) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), pickerBranchTimeout)
			defer cancel()
			b, _ := s.cfg.Branch(ctx, pr.Root)
			mu.Lock()
			out[pr.Root] = b
			mu.Unlock()
		}(pr)
	}
	wg.Wait()
	writeJSON(w, out)
}

package web

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gitbranch"
)

// The base is the branch R, I and C compare with (defaultBranch), as in
// the TUI (see ui/basepick.go). b in R and on the Checks page, or a click
// on the base where they or the status line name it, lists the branches
// to compare with; a pick saves it as the project's defaultBranch and
// applies at once, as a change on the settings page does. Every page of
// the project hears it as a "base" event.

// baseBranch is the base now.
func (p *projectState) baseBranch() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.base
}

// baseEvent tells the pages the base.
type baseEvent struct {
	DefaultBranch string `json:"defaultBranch"`
}

// applyBase makes name the base and tells every page; the engine's
// markers follow it once it is asked about it.
func (p *projectState) applyBase(name string) {
	p.mu.Lock()
	same := p.base == name
	p.base = name
	p.mu.Unlock()
	if same {
		return
	}
	p.hub.send("base", baseEvent{DefaultBranch: name})
	p.forgeCheck()
	if p.deps.ChangedActions != nil {
		go p.primeChanges()
	}
}

// baseRow is a branch as the base picker lists it.
type baseRow struct {
	Name string `json:"name"`
	// Base is set for the branch compared with now.
	Base bool   `json:"base,omitempty"`
	Tags string `json:"tags"`
}

// basesView is the base picker's list: the branches ?q matches, best
// first, of Total.
type basesView struct {
	Branches []baseRow `json:"branches"`
	Total    int       `json:"total"`
	Base     string    `json:"base"`
	NotGit   bool      `json:"notGit,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// baseBranches lists the branches the base can be.
func (p *projectState) baseBranches(ctx context.Context) ([]gitbranch.Branch, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	l, err := p.deps.Branches.List(ctx)
	if err != nil {
		return nil, err
	}
	return frontend.BaseBranches(l.Branches, p.baseBranch()), nil
}

func (p *projectState) bases(w http.ResponseWriter, r *http.Request) {
	base := p.baseBranch()
	v := basesView{Branches: []baseRow{}, Base: base}
	if p.deps.Branches == nil || p.deps.ChangedActions == nil {
		v.Error = "changing the base is not available"
		writeJSON(w, v)
		return
	}
	all, err := p.baseBranches(r.Context())
	switch {
	case errors.Is(err, gitbranch.ErrNotGit):
		v.NotGit = true
		writeJSON(w, v)
		return
	case err != nil:
		v.Error = err.Error()
		writeJSON(w, v)
		return
	}
	now := p.deps.Now()
	v.Total = len(all)
	for _, b := range frontend.FindBranches(all, r.URL.Query().Get("q")) {
		v.Branches = append(v.Branches, baseRow{Name: b.Name, Base: b.Name == base, Tags: frontend.BaseTags(b, base, now)})
	}
	writeJSON(w, v)
}

func (p *projectState) setBase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if p.deps.Branches == nil || p.deps.ChangedActions == nil {
		writeJSON(w, okView{Message: "changing the base is not available"})
		return
	}
	if req.Name == p.baseBranch() {
		writeJSON(w, okView{OK: true, Message: "already comparing with " + req.Name})
		return
	}
	all, err := p.baseBranches(r.Context())
	if err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	if !slices.ContainsFunc(all, func(b gitbranch.Branch) bool { return b.Name == req.Name }) {
		writeJSON(w, okView{Message: "no branch " + req.Name + " to compare with"})
		return
	}
	msg := frontend.SaveBase(p.deps.SaveBranch, req.Name)
	p.applyBase(req.Name)
	writeJSON(w, okView{OK: true, Message: msg})
}

package web

import (
	"bytes"
	"cmp"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// A projectSummary is what the projects list says of an open project
// while a tab shows another (ADR 0008): whether it compiles or runs, its
// errors, and the files tabs have unsaved changes in. Every tab is sent
// them in a "projects" event whenever one changes.
type projectSummary struct {
	Key          string   `json:"key"`
	Root         string   `json:"root"`
	Name         string   `json:"name"`
	Backend      string   `json:"backend"`
	Compiling    bool     `json:"compiling,omitempty"`
	Running      bool     `json:"running,omitempty"`
	Errors       int      `json:"errors,omitempty"`
	DryRunErrors int      `json:"dryRunErrors,omitempty"`
	Unsaved      []string `json:"unsaved,omitempty"`
	worktreeOf
}

// summaryOf sums up op.
func (s *Server) summaryOf(op *openProject) projectSummary {
	p := op.p
	p.mu.Lock()
	sum := projectSummary{Key: op.key, Root: p.deps.Root, Name: p.name, Backend: cmp.Or(p.tool, "dataform"),
		Compiling: p.st.Compiling, Running: p.run.running, Errors: len(p.st.Errors), worktreeOf: p.worktree}
	if p.st.Err != nil {
		sum.Errors = 1
	}
	errs, _ := p.dryRunErrorsLocked()
	sum.DryRunErrors = len(errs)
	p.mu.Unlock()
	if u := s.unsavedOf(p.deps.Root, ""); len(u) > 0 {
		sum.Unsaved = u
	}
	return sum
}

// summaries are the open projects, the one a page was on last first.
func (s *Server) summaries() []projectSummary {
	s.mu.Lock()
	ops := make([]*openProject, 0, len(s.open))
	for _, op := range s.open {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].active.After(ops[j].active) })
	s.mu.Unlock()
	out := make([]projectSummary, len(ops))
	roots := make([]string, len(ops))
	for i, op := range ops {
		out[i] = s.summaryOf(op)
		roots[i] = out[i].Root
	}
	for i := range out {
		out[i].Main = mainAs(out[i].Main, roots)
	}
	return out
}

// summaryWait gathers a burst of changes into one event.
const summaryWait = 200 * time.Millisecond

// projectsChanged sends every tab the summaries soon, unless they are as
// they were last sent.
func (s *Server) projectsChanged() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.summaryTimer != nil {
		return
	}
	s.summaryTimer = time.AfterFunc(summaryWait, func() {
		s.mu.Lock()
		s.summaryTimer = nil
		s.mu.Unlock()
		sums := s.summaries()
		data, _ := json.Marshal(sums)
		s.mu.Lock()
		same := bytes.Equal(data, s.summarySent)
		s.summarySent = data
		s.mu.Unlock()
		if !same {
			s.hub.send("projects", sums)
		}
	})
}

// openProjects answers the summaries.
func (s *Server) openProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.summaries())
}

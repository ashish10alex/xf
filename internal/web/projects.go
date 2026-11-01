package web

import (
	"cmp"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
	"github.com/ashish10alex/xf/internal/worktree"
)

// A Server holds every project a tab has opened, each open once however
// many tabs show it (ADR 0008). A tab names its project in its URL, by
// the project's key (?project=shop), and its requests carry the key in
// the projectHeader; its event stream, in its query. A project stays
// open, its engine warm, until it is closed (q, or del in the projects
// list) or xf web stops.

// projectHeader is the header a page's requests name its project in.
const projectHeader = "X-Xf-Project"

// openProject is a project the server holds open.
type openProject struct {
	p *projectState
	// key is what tabs name it by, fixed while it is open.
	key string
	// close lets go of its engine.
	close func()
	// active is when a page last booted on it or connected to it; s.mu
	// guards it.
	active time.Time
}

// errNotOpen is a request for a project that isn't open (it closed with
// no tab showing it): the page boots again, which opens it.
var errNotOpen = errors.New("this project is not open here: reload the page")

// keyOf is the key of the project at root, among the remembered projects
// and those open: its folder's name, or the folder as the picker shows it
// when another project has the same name. A standalone scratch is
// "scratch". A project in a worktree is its main checkout's project's
// key, @ and the worktree's folder (ADR 0032), and its name never makes
// another's key a path.
func (s *Server) keyOf(root string, remembered []session.Project, open []string) string {
	if i, ok := worktree.Of(root); ok {
		return s.keyOf(i.MainRoot(), remembered, open) + "@" + i.Name
	}
	name := lastElem(root)
	real := project.RealPath(root)
	clash := func(other string) bool {
		if lastElem(other) != name || other == root || project.RealPath(other) == real {
			return false
		}
		_, inWorktree := worktree.Of(other)
		return !inWorktree
	}
	for _, pr := range remembered {
		if clash(pr.Root) {
			return shortPath(root)
		}
	}
	for _, r := range open {
		if clash(r) {
			return shortPath(root)
		}
	}
	return name
}

func shortPath(root string) string {
	home, _ := os.UserHomeDir()
	if home != "" && (root == home || strings.HasPrefix(root, home+string(filepath.Separator))) {
		return "~" + root[len(home):]
	}
	return root
}

// openRootsLocked are the roots of the open projects; s.mu is held.
func (s *Server) openRootsLocked() []string {
	out := make([]string, 0, len(s.open))
	for r := range s.open {
		out = append(out, r)
	}
	return out
}

// pathOfKey is the folder a key that is a path names, "" for a name.
func pathOfKey(key string) string {
	if strings.HasPrefix(key, "~") {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		return filepath.Join(home, key[1:])
	}
	if filepath.IsAbs(key) {
		return filepath.Clean(key)
	}
	return ""
}

// openByKeyLocked is the open project key names: by the key it opened
// with, or a path to it; s.mu is held.
func (s *Server) openByKeyLocked(key string) *openProject {
	for _, op := range s.open {
		if op.key == key {
			return op
		}
	}
	if path := pathOfKey(key); path != "" {
		real := project.RealPath(path)
		for root, op := range s.open {
			if root == path || project.RealPath(root) == real {
				return op
			}
		}
	}
	return nil
}

// rememberedByKey is the remembered project key names, by its key or a
// path to it.
func (s *Server) rememberedByKey(key string, remembered []session.Project, open []string) (session.Project, bool) {
	path := pathOfKey(key)
	real := ""
	if path != "" {
		real = project.RealPath(path)
	}
	for _, pr := range remembered {
		if path != "" && (pr.Root == path || project.RealPath(pr.Root) == real) {
			return pr, true
		}
	}
	// A worktree's key can start as a path does (~/a/shop@feat-x).
	for _, pr := range remembered {
		if s.keyOf(pr.Root, remembered, open) == key {
			return pr, true
		}
	}
	return session.Project{}, false
}

// requestKey is the project a request names, in its header, "" for
// none. (Only boot and the event stream take it in the query: other
// requests' ?project is a BigQuery project.)
func requestKey(r *http.Request) string {
	return r.Header.Get(projectHeader)
}

// project is the open project a request names, or nil with a 409
// answered. A request that names none gets the one project open, when
// there is just one (a client other than the page, such as a test).
func (s *Server) project(w http.ResponseWriter, r *http.Request) *projectState {
	key := requestKey(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" && len(s.open) == 1 {
		for _, op := range s.open {
			return op.p
		}
	}
	if op := s.openByKeyLocked(key); op != nil {
		return op.p
	}
	if key == "" {
		http.Error(w, "no project is open", http.StatusConflict)
	} else {
		http.Error(w, errNotOpen.Error(), http.StatusConflict)
	}
	return nil
}

// projectOf is the open project a request names, nil for none.
func (s *Server) projectOf(r *http.Request) *openProject {
	key := requestKey(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		if len(s.open) == 1 {
			for _, op := range s.open {
				return op
			}
		}
		return nil
	}
	return s.openByKeyLocked(key)
}

// ensure is the project key names, opened when it isn't open yet: a tab
// on a project that closed opens it again, as does a link to one.
// Opening one is serialised, so two tabs opening it at once share it.
func (s *Server) ensure(key string) (*openProject, error) {
	s.mu.Lock()
	op := s.openByKeyLocked(key)
	s.mu.Unlock()
	if op != nil {
		return op, nil
	}
	s.opening.Lock()
	defer s.opening.Unlock()
	remembered := s.projects()
	s.mu.Lock()
	op = s.openByKeyLocked(key)
	open := s.openRootsLocked()
	s.mu.Unlock()
	if op != nil {
		return op, nil
	}
	pr, ok := s.rememberedByKey(key, remembered, open)
	if !ok || s.cfg.Open == nil {
		return nil, errors.New("xf has no project " + key)
	}
	return s.openLocked(pr)
}

// openRoot is the project at pr.Root, opened when it isn't open yet.
func (s *Server) openRoot(pr session.Project) (*openProject, error) {
	s.opening.Lock()
	defer s.opening.Unlock()
	return s.openLocked(pr)
}

// openLocked opens pr unless it is open; s.opening is held.
func (s *Server) openLocked(pr session.Project) (*openProject, error) {
	real := project.RealPath(pr.Root)
	s.mu.Lock()
	for root, op := range s.open {
		if root == pr.Root || project.RealPath(root) == real {
			s.mu.Unlock()
			return op, nil
		}
	}
	s.mu.Unlock()
	if s.cfg.Open == nil {
		return nil, errors.New("no such project")
	}
	s.linkWorktree(pr)
	opened, err := s.cfg.Open(pr)
	if err != nil {
		return nil, err
	}
	return s.adopt(opened), nil
}

// adopt holds opened open.
func (s *Server) adopt(opened *Project) *openProject {
	remembered := s.projects()
	h := newHub()
	// What a project's events change, the summaries other tabs show do.
	h.watch = func(name string) {
		if name == "state" || name == "run" {
			s.projectsChanged()
		}
	}
	p := newProjectState(opened.Deps, h, opened.SaveSession)
	p.name, p.worktree = projectName(p.deps.Root), worktreeView(p.deps.Root)
	p.editor = opened.Editor
	p.install = opened.InstallCLI
	p.forgeCfg = s.cfg.Forge
	p.setScratch(opened)
	if s.cfg.Settings != nil {
		p.openedWith = reopenValues(s.cfg.Settings.View(p.deps.Root, p.tool))
	}
	op := &openProject{p: p, close: opened.Close, active: time.Now()}
	p.runEnded = s.exit.runEnded
	p.credsCheck = s.credsChecked
	p.termEnv = s.cfg.TermEnv
	s.mu.Lock()
	op.key = cmp.Or(s.keyOf(p.deps.Root, remembered, s.openRootsLocked()), "project")
	if p.standalone {
		op.key = "scratch"
	}
	s.open[p.deps.Root] = op
	s.mu.Unlock()
	s.projectsChanged()
	return op
}

// touch notes that a page is on op now.
func (s *Server) touch(op *openProject) {
	s.mu.Lock()
	op.active = time.Now()
	s.mu.Unlock()
}

// openKeys are the keys of the open projects, the one a page was on last
// first, without except.
func (s *Server) openKeys(except *openProject) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ops := make([]*openProject, 0, len(s.open))
	for _, op := range s.open {
		if op != except {
			ops = append(ops, op)
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].active.After(ops[j].active) })
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.key
	}
	return out
}

// closedEvent tells the pages on a project that it closed, and which
// projects are still open (the last shown first), so each goes to the
// one it showed before.
type closedEvent struct {
	Key string `json:"key"`
	// By is the browser window that closed it, as the closer words it.
	By string `json:"by,omitempty"`
	// Why says why it closed when it wasn't closed for its own sake: its
	// worktree is being removed (ADR 0032).
	Why  string   `json:"why,omitempty"`
	Open []string `json:"open"`
}

// closeProject closes op for every page on it: they are told, and go to
// another project. A local run of it is cancelled, and its session saved.
func (s *Server) closeProject(op *openProject, by string) []string {
	return s.closeProjectWhy(op, by, "")
}

// closeProjectWhy is closeProject, saying why.
func (s *Server) closeProjectWhy(op *openProject, by, why string) []string {
	open := s.openKeys(op)
	op.p.hub.send("closed", closedEvent{Key: op.key, By: by, Why: why, Open: open})
	s.drop(op)
	return open
}

// drop closes op, unless another close or a reopen got there first.
func (s *Server) drop(op *openProject) bool {
	s.mu.Lock()
	if s.open[op.p.deps.Root] != op {
		s.mu.Unlock()
		return false
	}
	delete(s.open, op.p.deps.Root)
	s.mu.Unlock()
	op.p.close()
	if op.close != nil {
		op.close()
	}
	op.p.hub.close() // after "closed", its pages go elsewhere
	s.projectsChanged()
	return true
}

// running reports whether a run of p is going.
func (p *projectState) running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.run.running
}

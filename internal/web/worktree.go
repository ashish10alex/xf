package web

import (
	"cmp"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
	"github.com/ashish10alex/xf/internal/worktree"
)

// A branch opens in a git worktree with shift+enter in B (ADR 0032). The
// worktree's project is a project of its own (ADR 0008), keyed and named
// by its main checkout's project and its folder: dbt@feat-x. Opening any
// worktree's project links in what its main checkout's has untracked
// (node_modules, credentials, xf's config). Any linked worktree can be
// removed, xf's in the main checkout's .xf/worktrees or another's, never
// forced; the main checkout can't.

// worktreeTimeout bounds making a worktree, which checks out every file.
const worktreeTimeout = 2 * time.Minute

func (s *Server) worktreeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/worktree/open", s.openWorktree)
	mux.HandleFunc("POST /api/worktree/remove", s.removeWorktree)
	mux.HandleFunc("GET /api/picker/worktrees", s.pickerWorktrees)
	mux.HandleFunc("GET /api/worktrees", s.openWorktrees)
}

// projectName is the name a project goes by: its folder's, or in a
// worktree its main checkout's project's, @ and the worktree's folder.
func projectName(root string) string {
	if i, ok := worktree.Of(root); ok {
		return lastElem(i.MainRoot()) + "@" + i.Name
	}
	return lastElem(root)
}

// worktreeOf is what the lists say of a project in a worktree, to put it
// under its main checkout's project.
type worktreeOf struct {
	// Main is the main checkout's project's root, Worktree the worktree's
	// folder, and Own says xf made it, in .xf/worktrees.
	Main     string `json:"main,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Own      bool   `json:"own,omitempty"`
}

func worktreeView(root string) worktreeOf {
	i, ok := worktree.Of(root)
	if !ok {
		return worktreeOf{}
	}
	return worktreeOf{Main: i.MainRoot(), Worktree: i.Name, Own: i.Own()}
}

// mainAs is main as one of roots spells it, when one is it through
// another path: git writes the main checkout's path as it resolved it,
// and the page puts a worktree under the row whose root is its Main.
func mainAs(main string, roots []string) string {
	if main == "" || slices.Contains(roots, main) {
		return main
	}
	real := project.RealPath(main)
	for _, r := range roots {
		if project.RealPath(r) == real {
			return r
		}
	}
	return main
}

// linkWorktree links into a worktree's project what it lacks, before its
// engine compiles it; a project in no worktree is left as it is. A link
// that fails is no reason not to open: the compile says what's missing.
func (s *Server) linkWorktree(pr session.Project) {
	i, ok := worktree.Of(pr.Root)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if _, err := worktree.Link(ctx, i, pr.Backend); err != nil && s.cfg.Log != nil {
		s.cfg.Log("linking into the worktree " + i.Top + ": " + err.Error())
	}
}

// openWorktree is shift+enter on a branch in B, or enter in its list for
// the projects tab's "New worktree…": the branch's checkout, opened as a
// project. A branch checked out elsewhere opens there, as git checks a
// branch out in one worktree at a time; else xf makes a worktree for it.
// With new, a name no branch has is made a branch from this checkout's
// HEAD, in a new worktree. It answers the project's key.
func (s *Server) openWorktree(w http.ResponseWriter, r *http.Request) {
	p := s.project(w, r)
	if p == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
		New  bool   `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if p.deps.Branches == nil || s.cfg.Open == nil {
		writeJSON(w, okView{Message: "opening a branch in a worktree is not available"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), worktreeTimeout)
	defer cancel()
	root := p.deps.Root
	l, err := gitbranch.Repo{Dir: root}.List(ctx)
	if err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	how := worktree.Local
	var b gitbranch.Branch
	if i := slices.IndexFunc(l.Branches, func(b gitbranch.Branch) bool { return b.Name == req.Name }); i >= 0 {
		b = l.Branches[i]
		if b.Remote {
			how = worktree.Remote
		}
	} else if req.New && strings.TrimSpace(req.Name) != "" {
		b, how = gitbranch.Branch{Name: req.Name}, worktree.New
	} else {
		writeJSON(w, okView{Message: "no branch " + req.Name})
		return
	}
	if b.Current {
		writeJSON(w, okView{Message: "this checkout is on " + b.Name})
		return
	}
	rel, err := worktree.Prefix(ctx, root)
	if err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	top, made := b.Worktree, false
	if top == "" {
		if top, err = worktree.Add(ctx, root, b.Name, how); err != nil {
			writeJSON(w, okView{Message: err.Error()})
			return
		}
		made = true
	}
	at := filepath.Join(top, rel)
	if fi, err := os.Stat(at); err != nil || !fi.IsDir() {
		writeJSON(w, okView{Message: b.Name + " has no project at " + filepath.ToSlash(rel)})
		return
	}
	op, err := s.openRoot(session.Project{Root: at, Backend: p.tool})
	if err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	msg := b.Name + " is checked out in " + shortPath(top) + ": opened it"
	if made {
		msg = "made a worktree for " + b.Name + " in " + shortPath(top)
	}
	if how == worktree.New {
		msg = "made the branch " + b.Name + " and a worktree for it in " + shortPath(top)
	}
	writeJSON(w, struct {
		OK  bool   `json:"ok"`
		Key string `json:"key"`
		// Root is the project's, which the projects tab shows itself in.
		Root    string `json:"root"`
		Message string `json:"message"`
	}{true, op.key, op.p.deps.Root, msg})
}

// dirtyShown is how many of a worktree's changed files a refusal names.
const dirtyShown = 3

// removeWorktree removes the linked worktree, xf's or another's, that
// holds the project at root: the palette's "Worktree: remove this
// worktree", shift+del on its row in the projects list, or the projects
// tab's Remove Worktree, which all ask first. It refuses for a main
// checkout, while the project runs, a tab has unsaved changes to it, or
// git would, and then leaves the project open. Otherwise the project
// closes for every tab, the worktree goes and xf forgets it; the branch
// stays.
func (s *Server) removeWorktree(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Root string `json:"root"`
		// By is the browser window removing it, as the page words it.
		By string `json:"by"`
		// Force removes it with the changes git would lose, once the
		// person said so after an answer that named them.
		Force bool `json:"force"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	i, ok := worktree.Of(req.Root)
	if !ok {
		writeJSON(w, okView{Message: "not a worktree: " + shortPath(req.Root)})
		return
	}
	op := s.openAt(req.Root)
	if op != nil && op.p.running() {
		writeJSON(w, okView{Message: "a run of " + op.key + " is in progress: cancel it (ctrl+c) before removing its worktree"})
		return
	}
	if u := s.unsavedOf(req.Root, ""); len(u) > 0 {
		writeJSON(w, okView{Message: "a tab has unsaved changes in " + strings.Join(u, ", ") + ": save or discard them before removing the worktree"})
		return
	}
	backend := ""
	if op != nil {
		backend = op.p.tool
	}
	ctx, cancel := context.WithTimeout(r.Context(), worktreeTimeout)
	defer cancel()
	worktree.Unlink(i, backend)
	files, n, err := worktree.Dirty(ctx, i, dirtyShown)
	if err != nil {
		s.relink(i, backend)
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	if n > 0 && !req.Force {
		// The page asks whether to remove it anyway, losing them.
		more := ""
		if n > len(files) {
			more = " and " + strconv.Itoa(n-len(files)) + " more"
		}
		s.relink(i, backend)
		writeJSON(w, okView{Kind: "dirty", Message: "it has changes that removing it loses: " + strings.Join(files, ", ") + more})
		return
	}
	name := projectName(req.Root)
	open := s.openKeys(op)
	if op != nil {
		open = s.closeProjectWhy(op, req.By, "its worktree is being removed")
	}
	// The page that asked may be on the project, and leaves it at the
	// "closed" event, which ends this request: the removal goes on
	// without it.
	gone, cancelGone := context.WithTimeout(context.WithoutCancel(ctx), worktreeTimeout)
	defer cancelGone()
	if err := worktree.Remove(gone, i, req.Force); err != nil {
		s.relink(i, backend)
		if s.cfg.Log != nil {
			s.cfg.Log("removing the worktree " + i.Top + ": " + err.Error())
		}
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	for _, pr := range s.projects() {
		if pr.Root == req.Root || project.RealPath(pr.Root) == project.RealPath(req.Root) {
			_ = session.Forget(pr)
		}
	}
	writeJSON(w, struct {
		OK      bool     `json:"ok"`
		Message string   `json:"message"`
		Open    []string `json:"open"`
	}{true, "removed the worktree " + name + " (its branch stays)", open})
}

// relink puts back the links Unlink took, after a refusal.
func (s *Server) relink(i worktree.Info, backend string) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	_, _ = worktree.Link(ctx, i, backend)
}

// openAt is the open project at root, through any path to it, or nil.
func (s *Server) openAt(root string) *openProject {
	real := project.RealPath(root)
	s.mu.Lock()
	defer s.mu.Unlock()
	for r, op := range s.open {
		if r == root || project.RealPath(r) == real {
			return op
		}
	}
	return nil
}

// foundWorktree is a worktree git lists that xf has no project for yet,
// which the projects list shows under its main checkout's project.
type foundWorktree struct {
	Root string `json:"root"`
	// Path is Root as the picker shows it, and Main the main checkout's
	// project's root, as xf remembers it.
	Path     string `json:"path"`
	Main     string `json:"main"`
	Name     string `json:"name"`
	Branch   string `json:"branch"`
	Backend  string `json:"backend"`
	Worktree string `json:"worktree"`
	Own      bool   `json:"own,omitempty"`
}

// pickerWorktrees are, by the root of each remembered project in a main
// checkout, the other worktrees of its repository that hold the project
// and that xf doesn't remember, read at once.
func (s *Server) pickerWorktrees(w http.ResponseWriter, r *http.Request) {
	out := map[string][]foundWorktree{}
	remembered := s.projects()
	known := map[string]bool{}
	for _, pr := range remembered {
		known[project.RealPath(pr.Root)] = true
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, pr := range remembered {
		if _, ok := worktree.Of(pr.Root); ok {
			continue
		}
		wg.Add(1)
		go func(pr session.Project) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), pickerBranchTimeout)
			defer cancel()
			found := worktreesOf(ctx, pr, known)
			if len(found) > 0 {
				mu.Lock()
				out[pr.Root] = found
				mu.Unlock()
			}
		}(pr)
	}
	wg.Wait()
	writeJSON(w, out)
}

// openWorktrees are, by the root of each open project in a main checkout,
// the other worktrees of its repository that hold the project and aren't
// open, remembered or not: the column's projects tab lists them under it,
// to switch to.
func (s *Server) openWorktrees(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var mains []session.Project
	open := map[string]bool{}
	for root, op := range s.open {
		open[project.RealPath(root)] = true
		if op.p.worktree.Main == "" && !op.p.standalone {
			mains = append(mains, session.Project{Root: root, Backend: op.p.tool})
		}
	}
	s.mu.Unlock()
	out := map[string][]foundWorktree{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, pr := range mains {
		wg.Add(1)
		go func(pr session.Project) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), pickerBranchTimeout)
			defer cancel()
			if found := worktreesOf(ctx, pr, open); len(found) > 0 {
				mu.Lock()
				out[pr.Root] = found
				mu.Unlock()
			}
		}(pr)
	}
	wg.Wait()
	writeJSON(w, out)
}

// worktreesOf are the worktrees but the main checkout of pr's repository
// that hold pr's project, except those known.
func worktreesOf(ctx context.Context, pr session.Project, known map[string]bool) []foundWorktree {
	list, err := worktree.List(ctx, pr.Root)
	if err != nil || len(list) < 2 {
		return nil
	}
	rel, err := worktree.Prefix(ctx, pr.Root)
	if err != nil {
		return nil
	}
	var out []foundWorktree
	for _, e := range list[1:] {
		at := filepath.Join(e.Top, rel)
		if e.Prunable || known[project.RealPath(at)] {
			continue
		}
		if fi, err := os.Stat(at); err != nil || !fi.IsDir() {
			continue
		}
		v := worktreeView(at)
		out = append(out, foundWorktree{Root: at, Path: frontend.ShortPath(homeDir(), at), Main: pr.Root, Name: projectName(at), Branch: cmp.Or(e.Branch, "(detached)"),
			Backend: cmp.Or(pr.Backend, "dataform"), Worktree: v.Worktree, Own: v.Own})
	}
	return out
}

// branchWorktree is where B says a branch is checked out, "" for here or
// nowhere.
func branchWorktree(b gitbranch.Branch) string {
	if b.Worktree == "" {
		return ""
	}
	return frontend.ShortPath(homeDir(), b.Worktree)
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

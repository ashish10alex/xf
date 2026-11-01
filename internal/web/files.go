package web

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/backend/watch"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/trash"
)

// The file tree (ADR 0006): the project's files as git lists them, what
// the page marks them with, and making, renaming and deleting them. A
// watcher of its own tells the pages when files come and go, so a git
// checkout or a file made in a shell shows without a reload.

const (
	// maxTreeFiles bounds the files listed.
	maxTreeFiles = 20000
	// filesTimeout bounds listing the files.
	filesTimeout = 10 * time.Second
	// filesDebounce is how long the watcher waits for a burst of changes
	// (a checkout) to end.
	filesDebounce = 300 * time.Millisecond
)

// treeSkip are the folders a project's walk leaves out when git can't list
// its files: build output, packages and logs.
var treeSkip = []string{".git", "node_modules", "target", "dbt_packages", "logs"}

// treeSpec is what the tree's watcher watches: every file and folder,
// outside the folders treeSkip names at the root (and, as every watch,
// node_modules and dot folders).
var treeSpec = watch.Spec{Skip: treeSkip, AnyFile: true}

// fileRow is one file of the tree.
type fileRow struct {
	Path string `json:"path"`
	// Git is the file's git status: M changed, A added, U untracked, C in
	// conflict; "" unchanged.
	Git string `json:"git,omitempty"`
	// Action is the file's first action, which opening it selects, and
	// Actions how many it has.
	Action  project.ActionID `json:"action,omitempty"`
	Actions int              `json:"actions,omitempty"`
	// Errors counts the compile's errors in it.
	Errors int `json:"errors,omitempty"`
}

// filesView is the tree.
type filesView struct {
	Files []fileRow `json:"files"`
	// Dirs are the folders without files, which the files don't show.
	Dirs []string `json:"dirs"`
	// Git says the files are git's; else the project was walked.
	Git bool `json:"git"`
	// Truncated says more files were left out than maxTreeFiles.
	Truncated bool `json:"truncated,omitempty"`
}

// files answers with the tree.
func (p *projectState) files(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), filesTimeout)
	defer cancel()
	root := p.deps.Root
	files, dirs, isGit, err := gitFiles(ctx, root)
	if err != nil {
		files, dirs, err = walkFiles(root)
		isGit = false
	}
	if err != nil {
		writeError(w, err)
		return
	}
	v := filesView{Files: make([]fileRow, 0, min(len(files), maxTreeFiles)), Dirs: dirs, Git: isGit}
	if len(files) > maxTreeFiles {
		files, v.Truncated = files[:maxTreeFiles], true
	}
	var status map[string]string
	if isGit {
		status = gitStatus(ctx, root)
	}
	byFile := p.fileActions()
	p.mu.Lock()
	errs := map[string]int{}
	for _, e := range p.st.Errors {
		if e.FileName != "" {
			errs[filepath.ToSlash(e.FileName)]++
		}
	}
	p.mu.Unlock()
	for _, f := range files {
		row := fileRow{Path: f, Git: status[f], Errors: errs[f]}
		if fa, ok := byFile[f]; ok {
			row.Action, row.Actions = fa.first, fa.count
		}
		v.Files = append(v.Files, row)
	}
	writeJSON(w, v)
}

// actionOfFile answers with the first action of the file named, as the tree
// lists it: "" for a file with none. The page asks after each compile
// about a file it shows as having none, which this compile may give one.
func (p *projectState) actionOfFile(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Action project.ActionID `json:"action"`
	}
	v.Action = p.fileActions()[r.URL.Query().Get("file")].first
	writeJSON(w, v)
}

// fileAction is a file's first action, which opening it selects, and how
// many it has.
type fileAction struct {
	first project.ActionID
	count int
}

// fileActions maps the files the graph has actions in to theirs.
func (p *projectState) fileActions() map[string]fileAction {
	p.mu.Lock()
	defer p.mu.Unlock()
	byFile := map[string][]*project.Action{}
	if g := p.st.Graph; g != nil {
		for _, a := range g.Actions {
			if a.Kind != project.KindUnknown && a.FileName != "" {
				byFile[a.FileName] = append(byFile[a.FileName], a)
			}
		}
	}
	out := make(map[string]fileAction, len(byFile))
	for f, as := range byFile {
		// As ActionsInFile orders them: the main action first.
		first := slices.MinFunc(as, func(x, y *project.Action) int {
			return cmp.Or(cmp.Compare(x.Kind, y.Kind), cmp.Compare(x.ID, y.ID))
		})
		out[f] = fileAction{first.ID, len(as)}
	}
	return out
}

// gitFiles lists the files under root that git tracks and the ones it
// doesn't yet, without the ones it ignores or that are gone from disk,
// sorted, with the folders that hold no file.
func gitFiles(ctx context.Context, root string) (files, dirs []string, isGit bool, err error) {
	out, err := cmdrun.Git(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, nil, false, err
	}
	seen := map[string]bool{}
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f == "" || seen[f] {
			continue // a file in conflict is listed once per side
		}
		seen[f] = true
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
			continue // deleted, not yet committed
		}
		files = append(files, f)
	}
	slices.Sort(files)
	// --directory lists a folder git doesn't track as one entry, and the
	// empty ones too, which no file shows.
	if out, err := cmdrun.Git(ctx, root, "ls-files", "-z", "--others", "--exclude-standard", "--directory").Output(); err == nil {
		for d := range strings.SplitSeq(string(out), "\x00") {
			if !strings.HasSuffix(d, "/") {
				continue
			}
			d = strings.TrimSuffix(d, "/")
			i, _ := slices.BinarySearch(files, d+"/")
			if i < len(files) && strings.HasPrefix(files[i], d+"/") {
				// A new folder with files: its empty folders are in it.
				dirs = append(dirs, emptyDirsIn(root, d)...)
				continue
			}
			dirs = append(dirs, d)
		}
	}
	return files, nonNil(dirs), true, nil
}

// emptyDirsIn lists the folders under dir (relative to root) that hold
// nothing, outside the folders treeSkip names.
func emptyDirsIn(root, dir string) []string {
	var out []string
	top := filepath.Join(root, filepath.FromSlash(dir))
	_ = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == top {
			return nil
		}
		if slices.Contains(treeSkip, d.Name()) {
			return filepath.SkipDir
		}
		if entries, err := os.ReadDir(p); err == nil && len(entries) == 0 {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// gitStatus is git's status of the files under root, by path relative to
// it (see fileRow.Git).
func gitStatus(ctx context.Context, root string) map[string]string {
	prefix, err := cmdrun.Git(ctx, root, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return nil
	}
	pre := strings.TrimSpace(string(prefix))
	out, err := cmdrun.Git(ctx, root, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames", "--", ".").Output()
	if err != nil {
		return nil
	}
	st := map[string]string{}
	for e := range strings.SplitSeq(string(out), "\x00") {
		if len(e) < 4 {
			continue
		}
		x, y, f := e[0], e[1], e[3:]
		rel, ok := strings.CutPrefix(f, pre)
		if !ok {
			continue
		}
		switch {
		case x == '?':
			st[rel] = "U"
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			st[rel] = "C"
		case x == 'A':
			st[rel] = "A"
		case x == 'D' || y == 'D':
			// gone from disk: not listed
		default:
			st[rel] = "M"
		}
	}
	return st
}

// walkFiles lists the files under root when git can't: every one outside
// the folders treeSkip names and dot folders, sorted, with the folders
// that hold no file.
func walkFiles(root string) (files, dirs []string, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if slices.Contains(treeSkip, d.Name()) || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if entries, err := os.ReadDir(p); err == nil && len(entries) == 0 {
				dirs = append(dirs, rel)
			}
			return nil
		}
		if len(files) > maxTreeFiles {
			return filepath.SkipAll
		}
		files = append(files, rel)
		return nil
	})
	slices.Sort(files)
	return files, nonNil(dirs), err
}

// watchFiles tells the pages when files under the root come, go or
// change, until ctx is done.
func (p *projectState) watchFiles(ctx context.Context) {
	ch, err := watch.Watch(ctx, p.deps.Root, treeSpec, filesDebounce)
	if err != nil {
		return // the page lists the files again on focus
	}
	for range ch {
		p.hub.send("files", struct{}{})
	}
}

// ---- making, renaming and deleting ----

// newFile makes an empty file, or a folder, and the folders it is in.
func (p *projectState) newFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Dir  bool   `json:"dir"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	at, err := p.projectPath(req.Path)
	if err != nil {
		sourceError(w, err)
		return
	}
	if _, err := os.Lstat(at); err == nil {
		writeError(w, errors.New(req.Path+" already exists"))
		return
	}
	if req.Dir {
		err = os.MkdirAll(at, 0o755)
	} else if err = os.MkdirAll(filepath.Dir(at), 0o755); err == nil {
		var f *os.File
		if f, err = os.OpenFile(at, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
			err = f.Close()
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	p.hub.send("files", struct{}{})
	writeJSON(w, okView{OK: true, Message: "made " + req.Path})
}

// renamedAction is an action a rename renames: its ID and new name.
type renamedAction struct {
	ID   project.ActionID `json:"id"`
	Name string           `json:"name"`
}

// renameCheck is what a rename does besides moving the file: the actions
// it renames, and how many actions, in how many other files, refer to
// them by the old name.
type renameCheck struct {
	Renames []renamedAction `json:"renames"`
	Refs    int             `json:"refs"`
	Files   int             `json:"files"`
}

// checkRename works out the actions that renaming the file from to to
// renames: those named after their file, as Dataform names an action
// without config { name }, and dbt every model, seed and snapshot.
func (p *projectState) checkRename(from, to string) renameCheck {
	c := renameCheck{Renames: []renamedAction{}}
	stem := func(f string) string { b := path.Base(f); return strings.TrimSuffix(b, path.Ext(b)) }
	old, name := stem(from), stem(to)
	if old == name {
		return c
	}
	g := p.graph()
	if g == nil {
		return c
	}
	refs, files := map[project.ActionID]bool{}, map[string]bool{}
	for _, a := range g.ActionsInFile(from) {
		if a.Kind.External() || a.ParentAction != nil || a.Kind == project.KindTest {
			continue
		}
		if p.tool != "dbt" && cmp.Or(a.CanonicalTarget.Name, a.Target.Name) != old {
			continue // named in its config
		}
		c.Renames = append(c.Renames, renamedAction{ID: a.ID, Name: name})
		for _, d := range g.Dependents(a.ID) {
			if d.FileName != from && !refs[d.ID] {
				refs[d.ID] = true
				files[d.FileName] = true
			}
		}
	}
	c.Refs, c.Files = len(refs), len(files)
	return c
}

// renameFile renames or moves a file or folder, making the folders it
// goes into; with check it only answers what the rename would do.
func (p *projectState) renameFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From  string `json:"from"`
		To    string `json:"to"`
		Check bool   `json:"check"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	from, err := p.projectPath(req.From)
	if err != nil {
		sourceError(w, err)
		return
	}
	to, err := p.projectPath(req.To)
	if err != nil {
		sourceError(w, err)
		return
	}
	fi, err := os.Lstat(from)
	if err != nil {
		writeError(w, errors.New(req.From+" isn't there any more"))
		return
	}
	if req.To == req.From {
		writeError(w, errors.New("that's its name already"))
		return
	}
	if strings.HasPrefix(req.To, req.From+"/") {
		writeError(w, errors.New("can't move a folder into itself"))
		return
	}
	// On a disk that ignores case, renaming orders to Orders finds itself.
	if ti, err := os.Lstat(to); err == nil && !os.SameFile(fi, ti) {
		writeError(w, errors.New(req.To+" already exists"))
		return
	}
	if req.Check {
		c := renameCheck{Renames: []renamedAction{}}
		if !fi.IsDir() {
			c = p.checkRename(req.From, req.To)
		}
		writeJSON(w, c)
		return
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		writeError(w, err)
		return
	}
	if err := os.Rename(from, to); err != nil {
		writeError(w, err)
		return
	}
	p.hub.send("files", struct{}{})
	writeJSON(w, okView{OK: true, Message: "renamed " + req.From + " to " + req.To})
}

// deleteFile moves a file or folder to the system's trash.
func (p *projectState) deleteFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	at, err := p.projectPath(req.Path)
	if err != nil {
		sourceError(w, err)
		return
	}
	if err := trash.Move(at); err != nil {
		writeError(w, err)
		return
	}
	p.hub.send("files", struct{}{})
	writeJSON(w, okView{OK: true, Message: "moved " + req.Path + " to the trash"})
}

package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/gitscm"
	"github.com/ashish10alex/xf/internal/trash"
)

// Source Control (ADR 0007): the repository's changes, stashes and recent
// commits, the differences the source pane shows, and the git commands
// that stage, commit, discard, stash, push and pull. One command that
// changes something runs at a time; while it runs every page is told
// (the "scm" event, which also says when git's files change), and
// /api/scm/cancel stops it. Every such command and what it printed is
// kept for the git output.

type routeFunc = func(pattern string, h func(p *projectState, w http.ResponseWriter, r *http.Request))

func scmRoutes(route routeFunc) {
	route("GET /api/scm", (*projectState).scmStatus)
	route("GET /api/scm/log", (*projectState).scmLog)
	route("GET /api/scm/commit", (*projectState).scmCommitFiles)
	route("GET /api/scm/stash", (*projectState).scmStashFiles)
	route("GET /api/scm/diff", (*projectState).scmDiff)
	route("GET /api/scm/output", (*projectState).scmOutput)
	route("GET /api/scm/amend", (*projectState).scmAmend)
	route("POST /api/scm/stage", (*projectState).scmStage)
	route("POST /api/scm/unstage", (*projectState).scmUnstage)
	route("POST /api/scm/discard", (*projectState).scmDiscard)
	route("POST /api/scm/index", (*projectState).scmSetIndex)
	route("POST /api/scm/commit", (*projectState).scmCommit)
	route("POST /api/scm/push", (*projectState).scmPush)
	route("POST /api/scm/pull", (*projectState).scmPull)
	route("POST /api/scm/sync", (*projectState).scmSync)
	route("POST /api/scm/fetch", (*projectState).scmFetch)
	route("POST /api/scm/branch", (*projectState).scmNewBranch)
	route("POST /api/scm/stash/push", (*projectState).scmStashPush)
	route("POST /api/scm/stash/apply", (*projectState).scmStashApply)
	route("POST /api/scm/stash/drop", (*projectState).scmStashDrop)
	route("POST /api/scm/continue", (*projectState).scmContinue)
	route("POST /api/scm/abort", (*projectState).scmAbort)
	route("POST /api/scm/cancel", (*projectState).scmCancel)
}

// scmState is the git command running, and the git output.
type scmState struct {
	mu     sync.Mutex
	busy   string // what runs, as the header says it: "pushing"
	cancel context.CancelFunc
	out    []gitscm.Entry
}

// scmOutputMax is how many commands the git output keeps.
const scmOutputMax = 200

// scmTimeout bounds a command that only changes the repository; a
// command that goes to the network, or runs a commit's hooks, has
// scmNetTimeout.
const (
	scmTimeout    = 30 * time.Second
	scmNetTimeout = 3 * time.Minute
	scmLogPage    = 10
)

// scmEvent is the "scm" event: the page reads the status again.
type scmEvent struct {
	Busy string `json:"busy"`
}

func (p *projectState) sendSCM() {
	p.scm.mu.Lock()
	busy := p.scm.busy
	p.scm.mu.Unlock()
	p.hub.send("scm", scmEvent{Busy: busy})
}

// watchSCM tells the pages when git's files change, until ctx is done.
func (p *projectState) watchSCM(ctx context.Context) {
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		return
	}
	ch, err := g.Watch(ctx, 150*time.Millisecond)
	if err != nil {
		return // the page reads it again on focus and on file changes
	}
	for range ch {
		p.sendSCM()
	}
}

func (p *projectState) openGit(ctx context.Context) (*gitscm.Repo, error) {
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		return nil, err
	}
	g.OnWrite = func(e gitscm.Entry) {
		p.scm.mu.Lock()
		p.scm.out = append(p.scm.out, e)
		if n := len(p.scm.out); n > scmOutputMax {
			p.scm.out = p.scm.out[n-scmOutputMax:]
		}
		p.scm.mu.Unlock()
	}
	return g, nil
}

// projectFile is the path p (from the repository's top) relative to the
// project, "" for a file outside it.
func projectFile(g *gitscm.Repo, p string) string {
	if rest, ok := strings.CutPrefix(p, g.Prefix); ok {
		return rest
	}
	return ""
}

// ---- reading ----

// scmFile is a changed file: File is its path in the project, "" when it
// is outside the project.
type scmFile struct {
	gitscm.Change
	File string `json:"file,omitempty"`
}

type stashRow struct {
	gitscm.Stash
	Age string `json:"age"`
}

type scmView struct {
	NotGit bool   `json:"notGit,omitempty"`
	Error  string `json:"error,omitempty"`
	// Prefix is the project's folder in the repository, "" at its top.
	Prefix    string     `json:"prefix"`
	Branch    string     `json:"branch"`
	Head      string     `json:"head"`
	Upstream  string     `json:"upstream,omitempty"`
	Ahead     int        `json:"ahead"`
	Behind    int        `json:"behind"`
	Op        string     `json:"op,omitempty"`
	Staged    []scmFile  `json:"staged"`
	Changes   []scmFile  `json:"changes"`
	Conflicts []scmFile  `json:"conflicts"`
	Stashes   []stashRow `json:"stashes"`
	Busy      string     `json:"busy,omitempty"`
}

func filesOf(g *gitscm.Repo, cs []gitscm.Change) []scmFile {
	out := make([]scmFile, 0, len(cs))
	for _, c := range cs {
		out = append(out, scmFile{Change: c, File: projectFile(g, c.Path)})
	}
	return out
}

func (p *projectState) scmStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	p.scm.mu.Lock()
	busy := p.scm.busy
	p.scm.mu.Unlock()
	empty := scmView{Staged: []scmFile{}, Changes: []scmFile{}, Conflicts: []scmFile{}, Stashes: []stashRow{}, Busy: busy}
	g, err := gitscm.Open(ctx, p.deps.Root)
	if errors.Is(err, gitscm.ErrNotGit) {
		empty.NotGit = true
		writeJSON(w, empty)
		return
	}
	if err != nil {
		empty.Error = err.Error()
		writeJSON(w, empty)
		return
	}
	st, err := g.Status(ctx)
	if err != nil {
		empty.Error = err.Error()
		writeJSON(w, empty)
		return
	}
	v := scmView{Prefix: g.Prefix, Branch: st.Branch, Head: st.Head, Upstream: st.Upstream, Ahead: st.Ahead, Behind: st.Behind, Op: st.Op,
		Staged: filesOf(g, st.Staged), Changes: filesOf(g, st.Changes), Conflicts: filesOf(g, st.Conflicts), Stashes: []stashRow{}, Busy: busy}
	if list, err := g.Stashes(ctx); err == nil {
		now := p.deps.Now()
		for _, s := range list {
			v.Stashes = append(v.Stashes, stashRow{Stash: s, Age: gitbranch.Age(now, s.Date)})
		}
	}
	writeJSON(w, v)
}

type commitRow struct {
	gitscm.Commit
	Age string `json:"age"`
}

type logView struct {
	Commits []commitRow `json:"commits"`
	More    bool        `json:"more"`
}

func (p *projectState) scmLog(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		writeError(w, err)
		return
	}
	skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
	list, err := g.Log(ctx, max(skip, 0), scmLogPage+1)
	if err != nil {
		writeError(w, err)
		return
	}
	v := logView{Commits: []commitRow{}, More: len(list) > scmLogPage}
	now := p.deps.Now()
	for _, c := range list[:min(len(list), scmLogPage)] {
		v.Commits = append(v.Commits, commitRow{Commit: c, Age: gitbranch.Age(now, c.Date)})
	}
	writeJSON(w, v)
}

type filesListView struct {
	Files  []scmFile `json:"files"`
	Parent string    `json:"parent,omitempty"`
}

func (p *projectState) scmCommitFiles(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		writeError(w, err)
		return
	}
	files, parent, err := g.CommitFiles(ctx, r.URL.Query().Get("sha"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, filesListView{Files: filesOf(g, files), Parent: parent})
}

func (p *projectState) scmStashFiles(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		writeError(w, err)
		return
	}
	i, err := strconv.Atoi(r.URL.Query().Get("i"))
	if err != nil || i < 0 {
		writeError(w, errors.New("no stash "+r.URL.Query().Get("i")))
		return
	}
	files, err := g.StashFiles(ctx, i)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, filesListView{Files: filesOf(g, files)})
}

// diffView is a file's two sides, as the source pane shows them. Live
// says the new side is the file on disk in the project, which the source
// pane edits.
type diffView struct {
	Path     string      `json:"path"`
	File     string      `json:"file,omitempty"`
	Old      gitscm.Blob `json:"old"`
	New      gitscm.Blob `json:"new"`
	OldLabel string      `json:"oldLabel"`
	NewLabel string      `json:"newLabel"`
	Live     bool        `json:"live,omitempty"`
}

// scmDiff is ?path's difference: side "changes" (the index and the file
// on disk), "staged" (HEAD and the index), "commit" (?rev's first
// parent ?base and ?rev) or "stash" (stash ?rev's base and the stash).
func (p *projectState) scmDiff(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()
	path, rev := q.Get("path"), q.Get("rev")
	v := diffView{Path: path, File: projectFile(g, path)}
	show := func(rev string) gitscm.Blob {
		if err != nil {
			return gitscm.Blob{}
		}
		var b gitscm.Blob
		b, err = g.Show(ctx, rev, path, maxSourceBytes)
		return b
	}
	switch q.Get("side") {
	case "changes":
		v.Old, v.OldLabel, v.NewLabel = show(""), "index", "working tree"
		if err == nil {
			var at string
			if at, err = g.File(path); err == nil {
				if raw, rerr := os.ReadFile(at); rerr == nil {
					v.New = gitscm.BlobOf(raw, maxSourceBytes)
					v.Live = v.File != "" && !v.New.Binary && !v.New.TooBig
				}
			}
		}
	case "staged":
		v.Old, v.New, v.OldLabel, v.NewLabel = show("HEAD"), show(""), "HEAD", "index"
	case "commit":
		base := q.Get("base")
		if base != "" {
			v.Old = show(base)
			v.OldLabel = shortSHA(base)
		}
		v.New, v.NewLabel = show(rev), shortSHA(rev)
	case "stash":
		i, aerr := strconv.Atoi(rev)
		if aerr != nil || i < 0 {
			writeError(w, errors.New("no stash "+rev))
			return
		}
		ref := gitscm.StashRef(i)
		v.Old, v.New = show(ref+"^1"), show(ref)
		if err == nil && !v.New.Exists { // an untracked file it took
			v.New = show(ref + "^3")
		}
		v.OldLabel, v.NewLabel = "before "+ref, ref
	default:
		writeError(w, errors.New("no such difference"))
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, v)
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func (p *projectState) scmOutput(w http.ResponseWriter, r *http.Request) {
	p.scm.mu.Lock()
	out := append([]gitscm.Entry{}, p.scm.out...)
	p.scm.mu.Unlock()
	writeJSON(w, map[string]any{"entries": out})
}

// scmAmend is what amending starts from: HEAD's message, and whether
// HEAD is pushed, which refuses it.
func (p *projectState) scmAmend(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), gitTimeout)
	defer cancel()
	g, err := gitscm.Open(ctx, p.deps.Root)
	if err != nil {
		writeError(w, err)
		return
	}
	msg, err := g.HeadMessage(ctx)
	if err != nil {
		writeError(w, errors.New("no commit to amend yet"))
		return
	}
	writeJSON(w, map[string]any{"message": msg, "pushed": g.Pushed(ctx)})
}

// ---- changing ----

// scmRunBlocker is why a command that changes the files is refused while
// a local run goes.
const scmRunBlocker = "a run is in progress: this would change its files as it runs (ctrl+c cancels it)"

// scmDo runs f as the one git command that changes something, saying what
// (as the header does: "pushing"). Every page is told it starts and
// ends; the answer is f's message, or git's error. With files, a local
// run refuses it.
func (p *projectState) scmDo(w http.ResponseWriter, what string, timeout time.Duration, files bool, f func(ctx context.Context, g *gitscm.Repo) (string, error)) {
	if files {
		p.mu.Lock()
		local := p.run.running && !p.run.api
		p.mu.Unlock()
		if local {
			writeJSON(w, okView{Message: scmRunBlocker})
			return
		}
	}
	p.scm.mu.Lock()
	if p.scm.busy != "" {
		busy := p.scm.busy
		p.scm.mu.Unlock()
		writeJSON(w, okView{Message: "git is busy " + busy + ": wait for it, or stop it"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	p.scm.busy, p.scm.cancel = what, cancel
	ran := len(p.scm.out)
	p.scm.mu.Unlock()
	p.sendSCM()
	msg, err := func() (string, error) {
		g, err := p.openGit(ctx)
		if err != nil {
			return "", err
		}
		return f(ctx, g)
	}()
	p.scm.mu.Lock()
	p.scm.busy, p.scm.cancel = "", nil
	// Kind "git" says a git command failed, which the git output shows;
	// a refusal of xf's own (nothing staged) isn't one.
	failed := false
	for _, e := range p.scm.out[min(ran, len(p.scm.out)):] {
		failed = failed || !e.OK
	}
	p.scm.mu.Unlock()
	p.sendSCM()
	p.hub.send("files", struct{}{})
	go p.sendBranch()
	if err == nil && (what == "pushing" || what == "pulling" || what == "syncing" || what == "fetching") {
		// The host's merge request and pipeline may have moved with it.
		p.forgeCheck()
	}
	if err != nil {
		v := okView{Message: err.Error()}
		if failed {
			v.Kind = "git"
		}
		writeJSON(w, v)
		return
	}
	writeJSON(w, okView{OK: true, Message: msg})
}

func (p *projectState) scmCancel(w http.ResponseWriter, r *http.Request) {
	p.scm.mu.Lock()
	cancel := p.scm.cancel
	p.scm.mu.Unlock()
	if cancel == nil {
		writeJSON(w, okView{Message: "nothing is running"})
		return
	}
	cancel()
	writeJSON(w, okView{OK: true, Message: "stopping"})
}

// pathsReq names files, or all of a section's.
type pathsReq struct {
	Paths []string `json:"paths"`
	All   bool     `json:"all"`
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

func (p *projectState) scmStage(w http.ResponseWriter, r *http.Request) {
	var req pathsReq
	if !readJSON(w, r, &req) {
		return
	}
	p.scmDo(w, "staging", scmTimeout, false, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		if req.All {
			return "staged every change", g.StageAll(ctx)
		}
		return "staged " + one(req.Paths, "file"), g.Stage(ctx, req.Paths...)
	})
}

func (p *projectState) scmUnstage(w http.ResponseWriter, r *http.Request) {
	var req pathsReq
	if !readJSON(w, r, &req) {
		return
	}
	p.scmDo(w, "unstaging", scmTimeout, false, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		if req.All {
			return "unstaged every change", g.UnstageAll(ctx)
		}
		return "unstaged " + one(req.Paths, "file"), g.Unstage(ctx, req.Paths...)
	})
}

// one is the only path, or how many there are.
func one(paths []string, what string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return plural(len(paths), what)
}

// scmDiscard throws away the work tree's changes to the paths: a file git
// doesn't track goes to the trash; a tracked one is copied to the trash as
// it is, then put back as the index has it.
func (p *projectState) scmDiscard(w http.ResponseWriter, r *http.Request) {
	var req pathsReq
	if !readJSON(w, r, &req) {
		return
	}
	p.scmDo(w, "discarding", scmTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err != nil {
			return "", err
		}
		kinds := map[string]string{}
		for _, c := range st.Changes {
			kinds[c.Path] = c.Kind
		}
		paths := req.Paths
		if req.All {
			paths = nil
			for _, c := range st.Changes {
				paths = append(paths, c.Path)
			}
		}
		if len(paths) == 0 {
			return "", errors.New("no changes to discard")
		}
		var restore []string
		for _, path := range paths {
			kind, ok := kinds[path]
			if !ok {
				return "", errors.New(path + " has no change to discard")
			}
			at, err := g.File(path)
			if err != nil {
				return "", err
			}
			if kind == "U" {
				if err := trash.Move(at); err != nil {
					return "", err
				}
				continue
			}
			if _, err := os.Lstat(at); err == nil {
				if err := trashCopy(at); err != nil {
					return "", err
				}
			}
			restore = append(restore, path)
		}
		if len(restore) > 0 {
			if err := g.Restore(ctx, restore...); err != nil {
				return "", err
			}
		}
		return "discarded " + one(paths, "change") + ": the old version is in the trash", nil
	})
}

// trashCopy puts a copy of the file at path in the trash, by its name.
func trashCopy(path string) error {
	dir, err := os.MkdirTemp("", "xf-discard-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	to := filepath.Join(dir, filepath.Base(path))
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, to); err != nil {
			return err
		}
	} else {
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		_ = in.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return trash.Move(to)
}

// scmSetIndex is gsh: the index's version of a file becomes text.
func (p *projectState) scmSetIndex(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string  `json:"path"`
		Text string  `json:"text"`
		Was  *string `json:"was"`
		// Unstage words the answer for a change taken out of the index.
		Unstage bool `json:"unstage"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	what := "staging"
	if req.Unstage {
		what = "unstaging"
	}
	p.scmDo(w, what, scmTimeout, false, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		if err := g.SetIndex(ctx, req.Path, req.Text, req.Was); err != nil {
			return "", err
		}
		if req.Unstage {
			return "unstaged the change", nil
		}
		return "staged the change", nil
	})
}

func (p *projectState) scmCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
		Amend   bool   `json:"amend"`
		All     bool   `json:"all"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p.scmDo(w, "committing", scmNetTimeout, false, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err != nil {
			return "", err
		}
		if len(st.Conflicts) > 0 {
			return "", errors.New("files are in conflict: resolve them and stage them (space) first")
		}
		// A merge commits with git's message when the box has none.
		if strings.TrimSpace(req.Message) == "" && st.Op != "merge" {
			return "", errors.New("no commit message: type one in the box")
		}
		if st.Op == "merge" {
			if _, err := g.Continue(ctx, "merge", req.Message); err != nil {
				return "", err
			}
			return "committed the merge", nil
		}
		if req.Amend && g.Pushed(ctx) {
			return "", errors.New("HEAD is pushed: amending it would need a forced push, which xf never does")
		}
		if req.All {
			if err := g.StageAll(ctx); err != nil {
				return "", err
			}
		} else if !req.Amend && len(st.Staged) == 0 {
			if len(st.Changes) > 0 {
				return "", errors.New("nothing staged: stage files first (space), or use Commit all")
			}
			return "", errors.New("nothing to commit")
		}
		if _, err := g.Commit(ctx, req.Message, req.Amend); err != nil {
			return "", err
		}
		subject, _, _ := strings.Cut(strings.TrimSpace(req.Message), "\n")
		head := ""
		if st, err := g.Status(ctx); err == nil {
			head = st.Head + " "
		}
		if req.Amend {
			return "amended " + head + subject, nil
		}
		return "committed " + head + subject, nil
	})
}

// opBlocker refuses a command while an operation is in progress.
func opBlocker(st gitscm.Status) error {
	if st.Op != "" {
		return errors.New("a " + st.Op + " is in progress: finish it or abort it first")
	}
	return nil
}

func (p *projectState) scmPush(w http.ResponseWriter, r *http.Request) {
	p.scmDo(w, "pushing", scmNetTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err == nil {
			err = opBlocker(st)
		}
		if err != nil {
			return "", err
		}
		if _, err := g.Push(ctx, st); err != nil {
			return "", err
		}
		return "pushed " + st.Branch, nil
	})
}

func (p *projectState) scmPull(w http.ResponseWriter, r *http.Request) {
	p.scmDo(w, "pulling", scmNetTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err == nil {
			err = opBlocker(st)
		}
		if err != nil {
			return "", err
		}
		out, err := g.Pull(ctx)
		if err != nil {
			return "", err
		}
		if strings.Contains(out, "Already up to date") {
			return "already up to date", nil
		}
		return "pulled " + st.Branch, nil
	})
}

func (p *projectState) scmSync(w http.ResponseWriter, r *http.Request) {
	p.scmDo(w, "syncing", scmNetTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err == nil {
			err = opBlocker(st)
		}
		if err != nil {
			return "", err
		}
		if st.Upstream != "" {
			if _, err := g.Pull(ctx); err != nil {
				return "", err
			}
			if st, err = g.Status(ctx); err != nil {
				return "", err
			}
		}
		if _, err := g.Push(ctx, st); err != nil {
			return "", err
		}
		return "synced " + st.Branch, nil
	})
}

func (p *projectState) scmFetch(w http.ResponseWriter, r *http.Request) {
	p.scmDo(w, "fetching", scmNetTimeout, false, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		if _, err := g.Fetch(ctx); err != nil {
			return "", err
		}
		return "fetched origin", nil
	})
}

func (p *projectState) scmNewBranch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		From string `json:"from"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p.scmDo(w, "making a branch", scmTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		name := strings.TrimSpace(req.Name)
		if err := g.NewBranch(ctx, name, req.From); err != nil {
			return "", err
		}
		p.mu.Lock()
		watchOff := p.st.WatchErr != nil
		p.mu.Unlock()
		if watchOff && req.From != "" { // nothing else recompiles
			p.deps.Engine.Recompile("switched to " + name)
		}
		return "made " + name + " and switched to it", nil
	})
}

func (p *projectState) scmStashPush(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p.scmDo(w, "stashing", scmTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err == nil {
			err = opBlocker(st)
		}
		if err != nil {
			return "", err
		}
		if len(st.Changes)+len(st.Staged) == 0 {
			return "", errors.New("no changes to stash")
		}
		if _, err := g.StashPush(ctx, req.Message); err != nil {
			return "", err
		}
		return "stashed every change", nil
	})
}

func (p *projectState) scmStashApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int  `json:"index"`
		Pop   bool `json:"pop"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	what := "applying the stash"
	if req.Pop {
		what = "popping the stash"
	}
	p.scmDo(w, what, scmTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err == nil {
			err = opBlocker(st)
		}
		if err != nil {
			return "", err
		}
		ref := gitscm.StashRef(req.Index)
		if _, err := g.StashApply(ctx, req.Index, req.Pop); err != nil {
			if st, serr := g.Status(ctx); serr == nil && len(st.Conflicts) > 0 {
				return "", errors.New(ref + " applied with conflicts: resolve them in Merge Changes; the stash is kept")
			}
			return "", err
		}
		if req.Pop {
			return "popped " + ref, nil
		}
		return "applied " + ref, nil
	})
}

func (p *projectState) scmStashDrop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Index int `json:"index"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p.scmDo(w, "dropping the stash", scmTimeout, false, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		if _, err := g.StashDrop(ctx, req.Index); err != nil {
			return "", err
		}
		return "dropped " + gitscm.StashRef(req.Index), nil
	})
}

func (p *projectState) scmContinue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p.scmDo(w, "continuing", scmNetTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err != nil {
			return "", err
		}
		if st.Op == "" {
			return "", errors.New("nothing is in progress")
		}
		if len(st.Conflicts) > 0 {
			return "", errors.New("files are still in conflict: resolve them and stage them (space) first")
		}
		if _, err := g.Continue(ctx, st.Op, req.Message); err != nil {
			return "", err
		}
		return "finished the " + st.Op, nil
	})
}

func (p *projectState) scmAbort(w http.ResponseWriter, r *http.Request) {
	p.scmDo(w, "aborting", scmTimeout, true, func(ctx context.Context, g *gitscm.Repo) (string, error) {
		st, err := g.Status(ctx)
		if err != nil {
			return "", err
		}
		if _, err := g.Abort(ctx, st.Op); err != nil {
			return "", err
		}
		return "aborted the " + st.Op, nil
	})
}

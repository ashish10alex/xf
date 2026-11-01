// Package gitscm is xf web's Source Control (ADR 0007): a repository's
// status, its files as the index, HEAD, a commit or a stash has them, and
// the git commands that stage, commit, discard, stash, push and pull. Each
// is a plain git command; when git refuses, its message is the error.
// Nothing is ever forced, and git is never let ask for anything (a
// password, a passphrase, an editor), which nobody would see.
package gitscm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// ErrNotGit means the folder isn't in a git work tree.
var ErrNotGit = errors.New("not a git repository")

// remote is the remote a branch with no upstream is pushed to, as the
// branch switcher fetches.
const remote = "origin"

// Entry is a command that changed something, for the git output.
type Entry struct {
	Args []string  `json:"args"`
	Out  string    `json:"out"`
	OK   bool      `json:"ok"`
	At   time.Time `json:"at"`
	Took float64   `json:"took"` // seconds
}

// Repo is the work tree that holds a folder, opened by Open.
type Repo struct {
	// Top is the work tree's top folder, which every path is relative to.
	Top string
	// Prefix is the folder Open was given, from Top with a trailing "/",
	// or "" for Top itself.
	Prefix string
	// OnWrite, when set, is told every command that changes something.
	OnWrite func(Entry)
}

// Open opens the work tree that holds dir.
func Open(ctx context.Context, dir string) (*Repo, error) {
	out, err := cmdrun.Git(ctx, dir, "rev-parse", "--is-inside-work-tree", "--show-toplevel", "--show-prefix").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNotGit
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 || lines[0] != "true" {
		return nil, ErrNotGit
	}
	r := &Repo{Top: filepath.FromSlash(lines[1])}
	if len(lines) > 2 {
		r.Prefix = lines[2]
	}
	return r, nil
}

// read runs a git command that changes nothing and answers its output.
// --no-optional-locks keeps git status from rewriting the index, which
// would wake the watch (see Watch).
func (r *Repo) read(ctx context.Context, args ...string) (string, error) {
	out, err := cmdrun.Git(ctx, r.Top, append([]string{"--no-optional-locks"}, args...)...).Output()
	if err != nil {
		return "", gitError(args[0], err)
	}
	return string(out), nil
}

// gitError is git's own message for err, when it said one.
func gitError(cmd string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return errors.New(strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("git %s: %w", cmd, err)
}

// call is how write runs a command: what it reads on stdin, and whether it
// goes to the network.
type call struct {
	stdin string
	net   bool
}

// write runs a git command that changes something, tells OnWrite, and answers
// what it printed; when it fails that is the error.
func (r *Repo) write(ctx context.Context, c call, args ...string) (string, error) {
	cmd := cmdrun.Git(ctx, r.Top, args...)
	// No editor, password or passphrase prompt: nobody would see one.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_MERGE_AUTOEDIT=no")
	if c.net && os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	if c.stdin != "" {
		cmd.Stdin = strings.NewReader(c.stdin)
	}
	start := time.Now()
	out, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	if ctx.Err() == context.Canceled {
		text = strings.TrimSpace(text + "\nstopped")
		err = errors.New("stopped")
	} else if ctx.Err() == context.DeadlineExceeded {
		text = strings.TrimSpace(text + "\ntook too long: stopped")
		err = errors.New("git " + args[0] + " took too long: stopped")
	}
	if r.OnWrite != nil {
		r.OnWrite(Entry{Args: append([]string{"git"}, args...), Out: text, OK: err == nil, At: start, Took: time.Since(start).Seconds()})
	}
	if err != nil {
		if msg := strings.TrimSpace(text); msg != "" && ctx.Err() == nil {
			return text, errors.New(msg)
		}
		if ctx.Err() == nil {
			return text, fmt.Errorf("git %s: %w", args[0], err)
		}
		return text, err
	}
	return text, nil
}

// ---- the status ----

// Change is a changed file: its path from Top, and how it changed: M
// (modified), A (added), D (deleted), T (its type), U (untracked) or C
// (in conflict, XY saying how, as git status does: UU, AA, DU…).
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	XY   string `json:"xy,omitempty"`
}

// Status is the work tree's status.
type Status struct {
	// Branch is the checked-out branch, "" when HEAD is detached.
	Branch string `json:"branch"`
	// Head is HEAD's short SHA, "" before the first commit.
	Head     string `json:"head"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	// Staged are the index's changes from HEAD, Changes the work tree's
	// from the index (and its untracked files), Conflicts the files in
	// conflict.
	Staged    []Change `json:"staged"`
	Changes   []Change `json:"changes"`
	Conflicts []Change `json:"conflicts"`
	// Op is the operation in progress: "merge", "rebase", "cherry-pick",
	// "revert", or "".
	Op string `json:"op,omitempty"`
}

// Status reads the work tree's status.
func (r *Repo) Status(ctx context.Context) (Status, error) {
	out, err := r.read(ctx, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all", "--no-renames")
	if err != nil {
		return Status{}, err
	}
	st := parseStatus(out)
	st.Op = r.op(ctx)
	if st.Head != "" {
		if sha, err := r.read(ctx, "rev-parse", "--short", "HEAD"); err == nil {
			st.Head = strings.TrimSpace(sha)
		}
	}
	return st, nil
}

// parseStatus reads git status --porcelain=v2 -z --branch --no-renames.
func parseStatus(out string) Status {
	st := Status{Staged: []Change{}, Changes: []Change{}, Conflicts: []Change{}}
	for e := range strings.SplitSeq(out, "\x00") {
		if len(e) < 2 {
			continue
		}
		switch e[0] {
		case '#':
			f := strings.Fields(e)
			if len(f) < 3 {
				continue
			}
			switch f[1] {
			case "branch.oid":
				if f[2] != "(initial)" {
					st.Head = f[2]
				}
			case "branch.head":
				if f[2] != "(detached)" {
					st.Branch = f[2]
				}
			case "branch.upstream":
				st.Upstream = f[2]
			case "branch.ab":
				if len(f) >= 4 {
					st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[2], "+"))
					st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[3], "-"))
				}
			}
		case '1':
			f := strings.SplitN(e, " ", 9)
			if len(f) < 9 {
				continue
			}
			x, y := f[1][0], f[1][1]
			if x != '.' {
				st.Staged = append(st.Staged, Change{Path: f[8], Kind: string(x)})
			}
			if y != '.' {
				st.Changes = append(st.Changes, Change{Path: f[8], Kind: string(y)})
			}
		case 'u':
			f := strings.SplitN(e, " ", 11)
			if len(f) < 11 {
				continue
			}
			st.Conflicts = append(st.Conflicts, Change{Path: f[10], Kind: "C", XY: f[1]})
		case '?':
			st.Changes = append(st.Changes, Change{Path: e[2:], Kind: "U"})
		}
	}
	return st
}

// opFiles are the files in the git directory that say an operation is in
// progress, by the operation.
var opFiles = []struct{ file, op string }{
	{"rebase-merge", "rebase"},
	{"rebase-apply", "rebase"},
	{"MERGE_HEAD", "merge"},
	{"CHERRY_PICK_HEAD", "cherry-pick"},
	{"REVERT_HEAD", "revert"},
}

// op is the operation in progress, "" for none.
func (r *Repo) op(ctx context.Context) string {
	args := []string{"rev-parse"}
	for _, f := range opFiles {
		args = append(args, "--git-path", f.file)
	}
	out, err := r.read(ctx, args...)
	if err != nil {
		return ""
	}
	for i, p := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if i >= len(opFiles) {
			break
		}
		if _, err := os.Lstat(r.abs(p)); err == nil {
			return opFiles[i].op
		}
	}
	return ""
}

// abs is p, from Top unless it is absolute, as the OS writes it.
func (r *Repo) abs(p string) string {
	p = filepath.FromSlash(p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(r.Top, p)
}

// ---- reading files ----

// Blob is a file as a revision has it, its text with "\n" line ends and no
// byte-order mark, as the source pane's text is.
type Blob struct {
	Text   string `json:"text"`
	Exists bool   `json:"exists"`
	// Binary is set for a file that isn't text, TooBig for one bigger than
	// the limit Show was given; neither has its text.
	Binary bool `json:"binary,omitempty"`
	TooBig bool `json:"tooBig,omitempty"`
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// BlobOf is raw as a Blob, with max bytes at most.
func BlobOf(raw []byte, max int) Blob {
	b := Blob{Exists: true}
	switch {
	case len(raw) > max:
		b.TooBig = true
	case bytes.IndexByte(raw, 0) >= 0:
		b.Binary = true
	default:
		raw = bytes.TrimPrefix(raw, utf8BOM)
		b.Text = strings.ReplaceAll(string(raw), "\r\n", "\n")
	}
	return b
}

// Show is the file at p (from Top) as rev has it: "" is the index, else
// a commit, HEAD or a stash and its parents (stash@{0}^3). A file rev
// doesn't have is a Blob that doesn't exist.
func (r *Repo) Show(ctx context.Context, rev, p string, max int) (Blob, error) {
	if err := checkPath(p); err != nil {
		return Blob{}, err
	}
	if strings.HasPrefix(rev, "-") || strings.ContainsAny(rev, " :\x00") {
		return Blob{}, errors.New("no revision " + rev)
	}
	spec := rev + ":" + p
	if rev == "" {
		spec = ":0:" + p
	}
	// cat-file -e says whether it's there; show then reads it.
	if _, err := r.read(ctx, "cat-file", "-e", spec); err != nil {
		return Blob{}, nil
	}
	out, err := cmdrun.Git(ctx, r.Top, "--no-optional-locks", "cat-file", "blob", spec).Output()
	if err != nil {
		return Blob{}, gitError("cat-file", err)
	}
	return BlobOf(out, max), nil
}

// checkPath refuses a path that isn't a plain one from Top.
func checkPath(p string) error {
	if p == "" || path.IsAbs(p) || strings.HasPrefix(p, "-") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsRune(p, 0) {
		return errors.New("not a path in the repository: " + p)
	}
	if p == ".git" || strings.HasPrefix(p, ".git/") {
		return errors.New("not a path in the repository: " + p)
	}
	return nil
}

// File is the path of p (from Top) on disk, refused when it leaves the
// work tree through a symlink.
func (r *Repo) File(p string) (string, error) {
	if err := checkPath(p); err != nil {
		return "", err
	}
	at := filepath.Join(r.Top, filepath.FromSlash(p))
	if real, err := filepath.EvalSymlinks(filepath.Dir(at)); err == nil {
		top, _ := filepath.EvalSymlinks(r.Top)
		if rel, err := filepath.Rel(top, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", errors.New(p + " is outside the repository")
		}
	}
	return at, nil
}

// ---- the index ----

// Stage stages the paths as they are on disk, a deleted one as deleted.
func (r *Repo) Stage(ctx context.Context, paths ...string) error {
	if err := checkPaths(paths); err != nil {
		return err
	}
	_, err := r.write(ctx, call{}, append([]string{"add", "-A", "--"}, paths...)...)
	return err
}

// StageAll stages every change in the work tree, the untracked files too.
func (r *Repo) StageAll(ctx context.Context) error {
	_, err := r.write(ctx, call{}, "add", "-A", "--", ":/")
	return err
}

// Unstage puts the paths back in the index as HEAD has them; before the
// first commit, takes them out of it.
func (r *Repo) Unstage(ctx context.Context, paths ...string) error {
	if err := checkPaths(paths); err != nil {
		return err
	}
	if !r.hasHead(ctx) {
		_, err := r.write(ctx, call{}, append([]string{"rm", "--cached", "-r", "-q", "--"}, paths...)...)
		return err
	}
	_, err := r.write(ctx, call{}, append([]string{"restore", "--staged", "--"}, paths...)...)
	return err
}

// UnstageAll puts the index back as HEAD has it.
func (r *Repo) UnstageAll(ctx context.Context) error {
	if !r.hasHead(ctx) {
		_, err := r.write(ctx, call{}, "rm", "--cached", "-r", "-q", "--", ":/")
		return err
	}
	_, err := r.write(ctx, call{}, "restore", "--staged", "--", ":/")
	return err
}

func (r *Repo) hasHead(ctx context.Context) bool {
	_, err := r.read(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

func checkPaths(paths []string) error {
	if len(paths) == 0 {
		return errors.New("no files")
	}
	for _, p := range paths {
		if err := checkPath(p); err != nil {
			return err
		}
	}
	return nil
}

// ErrIndexChanged means the index's version of a file isn't the one a
// change was worked out from.
var ErrIndexChanged = errors.New("the index changed since: try again")

// SetIndex makes the index's version of p text, as gsh does when it
// stages one change: the page works out the text from the version it
// compared with, was (nil or "": not in the index), and the index must
// still have that one. text is written with the line ends and byte-order mark
// the index's version has (or, for a new one, the file's).
func (r *Repo) SetIndex(ctx context.Context, p, text string, was *string) error {
	if err := checkPath(p); err != nil {
		return err
	}
	out, err := r.read(ctx, "ls-files", "-s", "-z", "--", ":/"+p)
	if err != nil {
		return err
	}
	mode, raw := "", []byte(nil)
	if e, _, _ := strings.Cut(out, "\x00"); e != "" {
		meta, _, _ := strings.Cut(e, "\t")
		f := strings.Fields(meta)
		if len(f) != 3 {
			return errors.New("can't read the index's " + p)
		}
		if f[2] != "0" {
			return errors.New(p + " is in conflict: resolve it, then stage it whole")
		}
		mode = f[0]
		blob, err := cmdrun.Git(ctx, r.Top, "cat-file", "blob", f[1]).Output()
		if err != nil {
			return gitError("cat-file", err)
		}
		raw = blob
	}
	// The source pane's base for a file git doesn't track yet is "".
	if (mode == "" && was != nil && *was != "") || (mode != "" && was == nil) {
		return ErrIndexChanged
	}
	if mode != "" && BlobOf(raw, len(raw)+1).Text != *was {
		return ErrIndexChanged
	}
	like := raw
	if mode == "" {
		mode = "100644"
		at, err := r.File(p)
		if err != nil {
			return err
		}
		if fi, err := os.Stat(at); err == nil && fi.Mode()&0o111 != 0 {
			mode = "100755"
		}
		like, _ = os.ReadFile(at)
	}
	if mode == "120000" || mode == "160000" {
		return errors.New(p + " isn't a file: stage it whole")
	}
	body := strings.ReplaceAll(text, "\r\n", "\n")
	if bytes.Contains(like, []byte("\r\n")) {
		body = strings.ReplaceAll(body, "\n", "\r\n")
	}
	if bytes.HasPrefix(like, utf8BOM) {
		body = string(utf8BOM) + body
	}
	cmd := cmdrun.Git(ctx, r.Top, "hash-object", "-w", "--no-filters", "--stdin")
	cmd.Stdin = strings.NewReader(body)
	sha, err := cmd.Output()
	if err != nil {
		return gitError("hash-object", err)
	}
	_, err = r.write(ctx, call{}, "update-index", "--add", "--cacheinfo", mode+","+strings.TrimSpace(string(sha))+","+p)
	return err
}

// Restore puts the paths back on disk as the index has them.
func (r *Repo) Restore(ctx context.Context, paths ...string) error {
	if err := checkPaths(paths); err != nil {
		return err
	}
	_, err := r.write(ctx, call{}, append([]string{"restore", "--"}, paths...)...)
	return err
}

// ---- committing ----

// Commit commits what is staged with message; amend replaces HEAD instead.
// The repository's hooks run.
func (r *Repo) Commit(ctx context.Context, message string, amend bool) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errors.New("no commit message")
	}
	args := []string{"commit", "-F", "-"}
	if amend {
		args = append(args, "--amend")
	}
	return r.write(ctx, call{stdin: message}, args...)
}

// HeadMessage is HEAD's commit message.
func (r *Repo) HeadMessage(ctx context.Context) (string, error) {
	out, err := r.read(ctx, "log", "-1", "--format=%B", "HEAD")
	return strings.TrimRight(out, "\n"), err
}

// Pushed says whether HEAD is in its upstream, as git knew it at the last
// fetch: amending it then would need a forced push.
func (r *Repo) Pushed(ctx context.Context) bool {
	if _, err := r.read(ctx, "rev-parse", "--verify", "--quiet", "@{upstream}"); err != nil {
		return false
	}
	_, err := r.read(ctx, "merge-base", "--is-ancestor", "HEAD", "@{upstream}")
	return err == nil
}

// MergeMessage is the message git has ready for the merge in progress.
func (r *Repo) MergeMessage(ctx context.Context) string {
	p, err := r.read(ctx, "rev-parse", "--git-path", "MERGE_MSG")
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(r.abs(strings.TrimSpace(p)))
	if err != nil {
		return ""
	}
	var keep []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if !strings.HasPrefix(line, "#") {
			keep = append(keep, line)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// ---- remotes ----

// Push pushes the branch to its upstream, or with none to origin, which
// becomes its upstream. It is never forced.
func (r *Repo) Push(ctx context.Context, st Status) (string, error) {
	if st.Branch == "" {
		return "", errors.New("HEAD is detached: switch to a branch to push")
	}
	if st.Upstream == "" {
		return r.write(ctx, call{net: true}, "push", "-u", remote, st.Branch)
	}
	return r.write(ctx, call{net: true}, "push")
}

// Pull pulls the upstream's commits when the branch can move forward to
// them; when both have moved on git refuses, and merging is the shell's.
func (r *Repo) Pull(ctx context.Context) (string, error) {
	return r.write(ctx, call{net: true}, "pull", "--ff-only")
}

// Fetch fetches origin's branches, as the branch switcher does.
func (r *Repo) Fetch(ctx context.Context) (string, error) {
	return r.write(ctx, call{net: true}, "fetch", "--prune", remote)
}

// ---- history ----

// Commit is a commit as Recent commits lists it.
type Commit struct {
	SHA     string    `json:"sha"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
	// Unpushed is set for a commit its upstream doesn't have.
	Unpushed bool `json:"unpushed,omitempty"`
}

// Log is n of HEAD's commits after the first skip, newest first.
func (r *Repo) Log(ctx context.Context, skip, n int) ([]Commit, error) {
	if !r.hasHead(ctx) {
		return []Commit{}, nil
	}
	out, err := r.read(ctx, "log", "--skip="+strconv.Itoa(skip), "-n", strconv.Itoa(n), "--format=%H%x1f%h%x1f%s%x1f%an%x1f%ct%x1e", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	unpushed := map[string]bool{}
	if _, err := r.read(ctx, "rev-parse", "--verify", "--quiet", "@{upstream}"); err == nil {
		if u, err := r.read(ctx, "rev-list", "@{upstream}..HEAD"); err == nil {
			for s := range strings.FieldsSeq(u) {
				unpushed[s] = true
			}
		}
	}
	list := []Commit{}
	for rec := range strings.SplitSeq(out, "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
		if len(f) != 5 {
			continue
		}
		secs, _ := strconv.ParseInt(strings.TrimSpace(f[4]), 10, 64)
		list = append(list, Commit{SHA: f[0], Short: f[1], Subject: f[2], Author: f[3], Date: time.Unix(secs, 0), Unpushed: unpushed[f[0]]})
	}
	return list, nil
}

// CommitFiles are the files sha changed from its first parent, and that
// parent ("" for the first commit).
func (r *Repo) CommitFiles(ctx context.Context, sha string) ([]Change, string, error) {
	if !isSHA(sha) {
		return nil, "", errors.New("not a commit: " + sha)
	}
	parent := ""
	if out, err := r.read(ctx, "rev-parse", "--verify", "--quiet", sha+"^1"); err == nil {
		parent = strings.TrimSpace(out)
	}
	var out string
	var err error
	if parent != "" {
		out, err = r.read(ctx, "diff", "--name-status", "-z", "--no-renames", parent, sha, "--")
	} else {
		out, err = r.read(ctx, "diff-tree", "--root", "-r", "--no-commit-id", "--name-status", "-z", "--no-renames", sha)
	}
	if err != nil {
		return nil, "", err
	}
	return parseNameStatus(out), parent, nil
}

func isSHA(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

// parseNameStatus reads --name-status -z: a kind and a path, each ended
// by a NUL.
func parseNameStatus(out string) []Change {
	list := []Change{}
	f := strings.Split(out, "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		if f[i] == "" {
			break
		}
		list = append(list, Change{Path: f[i+1], Kind: f[i][:1]})
	}
	return list
}

// NewBranch makes branch name at from (a commit, or "" for HEAD) and
// switches to it; the working changes come along when git lets them.
func (r *Repo) NewBranch(ctx context.Context, name, from string) error {
	if _, err := r.read(ctx, "check-ref-format", "--branch", name); err != nil || strings.HasPrefix(name, "-") {
		return errors.New("not a branch name: " + name)
	}
	args := []string{"switch", "-c", name}
	if from != "" {
		if !isSHA(from) {
			return errors.New("not a commit: " + from)
		}
		args = append(args, from)
	}
	_, err := r.write(ctx, call{}, args...)
	return err
}

// ---- stashes ----

// Stash is a stash as the Stashes section lists it.
type Stash struct {
	Index   int       `json:"index"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
}

// Stashes lists the stashes, newest first.
func (r *Repo) Stashes(ctx context.Context) ([]Stash, error) {
	out, err := r.read(ctx, "stash", "list", "--format=%gd%x1f%gs%x1f%ct%x1e")
	if err != nil {
		return nil, err
	}
	list := []Stash{}
	for rec := range strings.SplitSeq(out, "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x1f")
		if len(f) != 3 {
			continue
		}
		i, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(f[0], "stash@{"), "}"))
		if err != nil {
			continue
		}
		secs, _ := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		list = append(list, Stash{Index: i, Subject: f[1], Date: time.Unix(secs, 0)})
	}
	return list, nil
}

// StashRef is stash i's name.
func StashRef(i int) string { return "stash@{" + strconv.Itoa(i) + "}" }

// StashFiles are the files stash i holds: the tracked ones it changed,
// and the untracked ones it took (kind U), whose text is in its third
// parent.
func (r *Repo) StashFiles(ctx context.Context, i int) ([]Change, error) {
	ref := StashRef(i)
	out, err := r.read(ctx, "diff", "--name-status", "-z", "--no-renames", ref+"^1", ref, "--")
	if err != nil {
		return nil, err
	}
	list := parseNameStatus(out)
	if _, err := r.read(ctx, "rev-parse", "--verify", "--quiet", ref+"^3"); err == nil {
		if out, err := r.read(ctx, "ls-tree", "-r", "--name-only", "-z", ref+"^3"); err == nil {
			for p := range strings.SplitSeq(out, "\x00") {
				if p != "" {
					list = append(list, Change{Path: p, Kind: "U"})
				}
			}
		}
	}
	return list, nil
}

// StashPush stashes every change, the untracked files too, with message
// when one is given.
func (r *Repo) StashPush(ctx context.Context, message string) (string, error) {
	args := []string{"stash", "push", "-u"}
	if m := strings.TrimSpace(message); m != "" {
		args = append(args, "-m", m)
	}
	return r.write(ctx, call{}, args...)
}

// StashApply applies stash i; pop drops it too once it applied cleanly.
func (r *Repo) StashApply(ctx context.Context, i int, pop bool) (string, error) {
	cmd := "apply"
	if pop {
		cmd = "pop"
	}
	return r.write(ctx, call{}, "stash", cmd, StashRef(i))
}

// StashDrop drops stash i.
func (r *Repo) StashDrop(ctx context.Context, i int) (string, error) {
	return r.write(ctx, call{}, "stash", "drop", StashRef(i))
}

// ---- operations in progress ----

// Continue finishes the operation in progress once its conflicts are
// resolved: a merge is committed, with message or git's own.
func (r *Repo) Continue(ctx context.Context, op, message string) (string, error) {
	switch op {
	case "merge":
		if strings.TrimSpace(message) != "" {
			return r.write(ctx, call{stdin: message}, "commit", "-F", "-")
		}
		return r.write(ctx, call{}, "commit", "--no-edit")
	case "rebase", "cherry-pick", "revert":
		return r.write(ctx, call{}, op, "--continue")
	}
	return "", errors.New("nothing is in progress")
}

// Abort stops the operation in progress, putting things back as they
// were before it.
func (r *Repo) Abort(ctx context.Context, op string) (string, error) {
	switch op {
	case "merge", "rebase", "cherry-pick", "revert":
		return r.write(ctx, call{}, op, "--abort")
	}
	return "", errors.New("nothing is in progress")
}

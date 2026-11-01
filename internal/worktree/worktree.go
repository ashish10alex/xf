// Package worktree finds, makes, links and removes the git worktrees xf
// web opens a branch in (ADR 0032). xf makes its own in the main
// checkout's .xf/worktrees, which ignores itself in git; it opens any
// other worktree too, such as an agent session's.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// Dir is where xf makes worktrees, from the main checkout's top.
const Dir = ".xf/worktrees"

// remote is the remote a branch only it has is checked out from, as
// gitbranch's switch does.
const remote = "origin"

// Info is a project in a linked worktree: the worktree, its main
// checkout, and where the project is in both.
type Info struct {
	// Top is the worktree's top folder and Name its folder's name.
	Top, Name string
	// MainTop is the main checkout's top folder.
	MainTop string
	// Rel is the project's path from either top, "." at the top.
	Rel string
}

// Root is the project's folder in the worktree.
func (i Info) Root() string { return filepath.Join(i.Top, i.Rel) }

// MainRoot is the project's folder in the main checkout.
func (i Info) MainRoot() string { return filepath.Join(i.MainTop, i.Rel) }

// Own reports whether xf made the worktree, in the main checkout's Dir.
func (i Info) Own() bool {
	return samePath(filepath.Dir(i.Top), filepath.Join(i.MainTop, filepath.FromSlash(Dir)))
}

// samePath reports whether a and b are one folder, through symlinks
// (macOS's /var is /private/var): git writes the paths it resolved.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// gitdirRe is a linked worktree's .git file, which names its folder in
// the main checkout's .git/worktrees.
var gitdirRe = regexp.MustCompile(`^gitdir: (.+)$`)

// Of is the worktree root is in, false when it is in a main checkout or
// none. It reads the .git file at the worktree's top, so it runs no git.
func Of(root string) (Info, bool) {
	root = filepath.Clean(root)
	for dir := root; ; {
		fi, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			if fi.IsDir() {
				return Info{}, false
			}
			return fromGitFile(dir, root)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Info{}, false
		}
		dir = parent
	}
}

// fromGitFile reads the .git file at top, the worktree's top, for root.
// A submodule's .git file names .git/modules, so it isn't a worktree.
func fromGitFile(top, root string) (Info, bool) {
	b, err := os.ReadFile(filepath.Join(top, ".git"))
	if err != nil {
		return Info{}, false
	}
	m := gitdirRe.FindStringSubmatch(strings.TrimSpace(string(b)))
	if m == nil {
		return Info{}, false
	}
	gitdir := filepath.FromSlash(m[1])
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(top, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	wts := filepath.Dir(gitdir)
	if filepath.Base(wts) != "worktrees" || filepath.Base(filepath.Dir(wts)) != ".git" {
		return Info{}, false
	}
	rel, err := filepath.Rel(top, root)
	if err != nil {
		return Info{}, false
	}
	return Info{Top: top, Name: filepath.Base(top), MainTop: filepath.Dir(filepath.Dir(wts)), Rel: rel}, true
}

// Entry is a worktree git lists.
type Entry struct {
	Top string
	// Branch is the branch checked out, "" when HEAD is detached.
	Branch string
	// Main is set for the main checkout, which git lists first.
	Main bool
	// Prunable is a worktree whose folder is gone.
	Prunable bool
}

// List is the worktrees of the repository dir is in, the main checkout
// first: `git worktree list --porcelain`.
func List(ctx context.Context, dir string) ([]Entry, error) {
	out, err := cmdrun.Git(ctx, dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil, gitError("worktree list", err)
	}
	return parseList(string(out)), nil
}

// parseList reads List's porcelain lines. A bare repository's entry is
// left out: it has no files.
func parseList(out string) []Entry {
	var list []Entry
	var e *Entry
	bare := false
	flush := func() {
		if e != nil && !bare {
			e.Main = len(list) == 0
			list = append(list, *e)
		}
		e, bare = nil, false
	}
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		field, value, _ := strings.Cut(line, " ")
		switch field {
		case "worktree":
			flush()
			e = &Entry{Top: filepath.FromSlash(value)}
		case "branch":
			if e != nil {
				e.Branch = strings.TrimPrefix(value, "refs/heads/")
			}
		case "bare":
			bare = true
		case "prunable":
			if e != nil {
				e.Prunable = true
			}
		}
	}
	flush()
	return list
}

// Prefix is the path from the top of dir's checkout to dir, "." at the
// top.
func Prefix(ctx context.Context, dir string) (string, error) {
	out, err := cmdrun.Git(ctx, dir, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return "", gitError("rev-parse", err)
	}
	p := strings.TrimSuffix(strings.TrimSpace(string(out)), "/")
	if p == "" {
		return ".", nil
	}
	return filepath.FromSlash(p), nil
}

// How is what branch Add checks out.
type How int

const (
	// Local is a local branch.
	Local How = iota
	// Remote is a branch only origin has, which gets a local branch that
	// tracks it.
	Remote
	// New is a branch made from the HEAD of dir's checkout, as B's
	// "+ make branch" makes one.
	New
)

// Add checks branch out in a new worktree in the main checkout's Dir,
// whichever checkout of the repository dir is in, and answers its top.
// how says what branch it is. When git refuses, as it does for a branch
// checked out elsewhere or a new branch's name that is taken, the error
// is git's message.
func Add(ctx context.Context, dir, branch string, how How) (string, error) {
	list, err := List(ctx, dir)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", errors.New("git lists no main checkout")
	}
	parent := filepath.Join(list[0].Top, filepath.FromSlash(Dir))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	ignore := filepath.Join(parent, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(ignore, []byte("# worktrees xf web made: not for git\n*\n"), 0o644)
	}
	top := freeFolder(parent, Slug(branch))
	args := []string{"worktree", "add", top, branch}
	switch how {
	case Remote:
		args = []string{"worktree", "add", "--track", "-b", branch, top, remote + "/" + branch}
	case New:
		args = []string{"worktree", "add", "-b", branch, top, "HEAD"}
	}
	if out, err := cmdrun.Git(ctx, dir, args...).CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("git worktree add: %w", err)
	}
	return top, nil
}

var unsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Slug is branch as a folder's name: what a folder can't hold, / among
// it, becomes -.
func Slug(branch string) string {
	s := strings.Trim(unsafeRe.ReplaceAllString(branch, "-"), "-.")
	if s == "" {
		return "worktree"
	}
	return s
}

// freeFolder is parent/name, or parent/name-2, -3 and so on when that is
// taken.
func freeFolder(parent, name string) string {
	for i := 1; ; i++ {
		p := filepath.Join(parent, name)
		if i > 1 {
			p += "-" + strconv.Itoa(i)
		}
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
	}
}

// Links are the files and folders a worktree's project gets from the
// main checkout's, for the backend ("" for every backend's): what is
// installed or local to a person, which git doesn't hold.
func Links(backend string) []string {
	dataform := []string{"node_modules", ".df-credentials.json"}
	dbt := []string{"dbt_packages", "profiles.yml", ".user.yml"}
	both := []string{filepath.Join(".xf", "config.json")}
	switch backend {
	case "dataform":
		return append(dataform, both...)
	case "dbt":
		return append(dbt, both...)
	}
	return append(append(dataform, dbt...), both...)
}

// hardLinked are those of Links that Link makes a folder of hard links
// to the main checkout's files, not a link: Dataform loads a module only
// when its real path is in the project, so it can't find @dataform/core
// through a node_modules that links to the main checkout's.
var hardLinked = map[string]bool{"node_modules": true}

// Link links into the worktree's project each of Links the main
// checkout's project has untracked and the worktree's lacks, and answers
// those it linked. It never replaces anything but a link of its own
// where a hard-linked folder belongs, as an older xf made. On Windows,
// where a symlink may need Developer Mode, a folder is a junction and a
// file a copy.
func Link(ctx context.Context, i Info, backend string) ([]string, error) {
	var want []string
	for _, p := range Links(backend) {
		dst := filepath.Join(i.Root(), p)
		if hardLinked[p] && linkedTo(filepath.Join(i.MainRoot(), p), dst) {
			_ = os.Remove(dst)
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(i.MainRoot(), p)); err != nil {
			continue
		}
		want = append(want, p)
	}
	if len(want) == 0 {
		return nil, nil
	}
	tracked, err := trackedIn(ctx, i.MainRoot(), want)
	if err != nil {
		return nil, err
	}
	var linked []string
	var errs []error
	for _, p := range want {
		if tracked[filepath.ToSlash(p)] {
			continue
		}
		src, dst := filepath.Join(i.MainRoot(), p), filepath.Join(i.Root(), p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			errs = append(errs, err)
			continue
		}
		mk := link
		if hardLinked[p] {
			mk = linkTree
		}
		if err := mk(src, dst); err != nil {
			errs = append(errs, fmt.Errorf("link %s: %w", p, err))
			continue
		}
		linked = append(linked, p)
	}
	if err := excludeLinkedFolders(ctx, i, linked); err != nil {
		errs = append(errs, err)
	}
	return linked, errors.Join(errs...)
}

// excludeLinkedFolders keeps the folders Link linked out of git status
// in the worktree. The main checkout ignores them with a folder's pattern
// (node_modules/), which a link, not being a folder, doesn't match; so
// each that the main checkout ignores gets an anchored line in the
// repository's info/exclude, which every checkout shares. The main
// checkout ignored them already, so it sees no change.
func excludeLinkedFolders(ctx context.Context, i Info, linked []string) error {
	var folders []string
	for _, p := range linked {
		if fi, err := os.Stat(filepath.Join(i.MainRoot(), p)); err == nil && fi.IsDir() {
			folders = append(folders, p)
		}
	}
	if len(folders) == 0 {
		return nil
	}
	// check-ignore exits 1 when it ignores none of them.
	out, _ := cmdrun.Git(ctx, i.MainRoot(), append([]string{"check-ignore", "--"}, folders...)...).Output()
	var lines []string
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			lines = append(lines, "/"+filepath.ToSlash(filepath.Join(i.Rel, p)))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	common, err := cmdrun.Git(ctx, i.MainTop, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return gitError("rev-parse", err)
	}
	dir := filepath.FromSlash(strings.TrimSpace(string(common)))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(i.MainTop, dir)
	}
	return appendLines(filepath.Join(dir, "info", "exclude"), "# folders xf web links into worktrees (ADR 0032)", lines)
}

// appendLines adds to file those of lines it doesn't have, under head.
func appendLines(file, head string, lines []string) error {
	b, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var add strings.Builder
	for _, l := range lines {
		if !have[l] {
			if add.Len() == 0 && !have[head] {
				add.WriteString(head + "\n")
			}
			add.WriteString(l + "\n")
		}
	}
	if add.Len() == 0 {
		return nil
	}
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		b = append(b, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, append(b, add.String()...), 0o644)
}

// trackedIn are those of paths, from dir, that git tracks, as git writes
// them (with /). A folder is tracked when a file in it is.
func trackedIn(ctx context.Context, dir string, paths []string) (map[string]bool, error) {
	args := append([]string{"ls-files", "-z", "--"}, paths...)
	out, err := cmdrun.Git(ctx, dir, args...).Output()
	if err != nil {
		return nil, gitError("ls-files", err)
	}
	tracked := map[string]bool{}
	for _, f := range strings.Split(string(out), "\x00") {
		for _, p := range paths {
			p = filepath.ToSlash(p)
			if f == p || strings.HasPrefix(f, p+"/") {
				tracked[p] = true
			}
		}
	}
	return tracked, nil
}

// link makes dst stand for src.
func link(src, dst string) error {
	if runtime.GOOS != "windows" {
		return os.Symlink(src, dst)
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		out, err := exec.Command("cmd", "/c", "mklink", "/J", dst, src).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return copyFile(src, dst)
}

// linkTree makes dst the folder src with each file a hard link to src's,
// which takes no room, or a copy where it can't be one. Links in src are
// made again as they are. It is made beside dst and then moved there, so
// a tree cut short is never taken for a whole one.
func linkTree(src, dst string) error {
	tmp := dst + ".xf-linking"
	_ = os.RemoveAll(tmp)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(tmp, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			t, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(t, to)
		case d.Type().IsRegular():
			if os.Link(path, to) == nil {
				return nil
			}
			return copyFile(path, to)
		}
		return nil
	})
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
	}
	return err
}

// copyFile writes src's bytes to dst, with src's permissions.
func copyFile(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, fi.Mode().Perm())
}

// Unlink takes away what Link made: links to the main checkout's files
// and, on Windows, copies still the same as them. A .xf folder Link made
// goes too, when nothing else is in it. It answers what it took away. A
// hard-linked folder stays: git, which ignores it as the main checkout
// does, removes it with the worktree, and the main checkout's files stay.
func Unlink(i Info, backend string) []string {
	var gone []string
	for _, p := range Links(backend) {
		src, dst := filepath.Join(i.MainRoot(), p), filepath.Join(i.Root(), p)
		if !linkedTo(src, dst) {
			continue
		}
		if err := os.Remove(dst); err == nil {
			gone = append(gone, p)
		}
		if d := filepath.Dir(p); d != "." {
			_ = os.Remove(filepath.Join(i.Root(), d)) // only when empty
		}
	}
	return gone
}

// linkedTo reports whether dst is a link to src, or on Windows a copy of
// the file src still is.
func linkedTo(src, dst string) bool {
	if t, err := os.Readlink(dst); err == nil {
		return filepath.Clean(t) == filepath.Clean(src)
	}
	if runtime.GOOS != "windows" {
		return false
	}
	fi, err := os.Lstat(dst)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	a, err1 := os.ReadFile(src)
	b, err2 := os.ReadFile(dst)
	return err1 == nil && err2 == nil && bytes.Equal(a, b)
}

// Dirty are the worktree's changed and untracked files as git status
// shows them (ignored ones aren't), at most limit of them, and how many
// there are.
func Dirty(ctx context.Context, i Info, limit int) ([]string, int, error) {
	out, err := cmdrun.Git(ctx, i.Top, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=normal").Output()
	if err != nil {
		return nil, 0, gitError("status", err)
	}
	var files []string
	n := 0
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		n++
		if len(files) < limit {
			files = append(files, line[3:])
		}
	}
	return files, n, nil
}

// Remove removes a linked worktree, xf's or another's, with `git worktree
// remove`: when git refuses, as it does for one with changes or one that
// is locked, the error is its message. force, which the person confirms
// first, removes one with changes, losing them; a locked one stays
// refused. The branch stays. A main checkout is never an Info, so it
// can't be removed.
func Remove(ctx context.Context, i Info, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	out, err := cmdrun.Git(ctx, i.MainTop, append(args, i.Top)...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("git worktree remove: %w", err)
	}
	return nil
}

// gitError is git's own message for err, when it said one.
func gitError(cmd string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(strings.TrimSpace(string(ee.Stderr))) > 0 {
		return errors.New(strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("git %s: %w", cmd, err)
}

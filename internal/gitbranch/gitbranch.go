// Package gitbranch lists the branches of a project's checkout, switches
// between them and fetches origin's, for the B switcher of the TUI and xf
// web. It never stashes, forces or discards: a switch is a plain git
// switch, and when git refuses its message is the error.
package gitbranch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// ErrNotGit means the project isn't in a git repository.
var ErrNotGit = errors.New("not a git repository")

// remote is the remote whose branches are listed, as API runs and R use.
const remote = "origin"

// Branch is a branch the switcher lists.
type Branch struct {
	// Name is the branch's name, without "origin/" for a remote one.
	Name string `json:"name"`
	// Remote is set for a branch only origin has (as of the last fetch):
	// a switch to it makes a local branch that tracks it.
	Remote bool `json:"remote,omitempty"`
	// Current is set for the checked-out branch.
	Current bool `json:"current,omitempty"`
	// Date is when the branch's tip was committed.
	Date time.Time `json:"date"`
	// OriginDate is when origin/<name>'s tip was committed, as of the last
	// fetch; zero when origin has no such branch. R compares with origin's
	// branch when there is one.
	OriginDate time.Time `json:"originDate,omitempty"`
	// Worktree is the top of the other worktree the branch is checked out
	// in, "" when it is in none or in this one: git checks a branch out
	// in one worktree at a time.
	Worktree string `json:"worktree,omitempty"`
}

// Listing is the checkout's branches, newest commit first.
type Listing struct {
	Branches []Branch
	// Detached is HEAD's short SHA when no branch is checked out.
	Detached string
	// Root is the checkout's top directory, which a switch changes all of.
	Root string
}

// Repo is the checkout that holds Dir.
type Repo struct{ Dir string }

func (r Repo) git(ctx context.Context, args ...string) (string, error) {
	out, err := cmdrun.Git(ctx, r.Dir, args...).Output()
	if err != nil {
		return "", gitError(args[0], err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// gitError is git's own message for err, when it said one.
func gitError(cmd string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(strings.TrimSpace(string(ee.Stderr))) > 0 {
		return errors.New(strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("git %s: %w", cmd, err)
}

// isRepo reports whether Dir is in a git work tree.
func (r Repo) isRepo(ctx context.Context) error {
	if out, err := r.git(ctx, "rev-parse", "--is-inside-work-tree"); err != nil || out != "true" {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrNotGit
	}
	return nil
}

// Current is the checked-out branch, or "(detached abc1234)", as the
// projects picker shows it. It is ErrNotGit outside a repository.
func (r Repo) Current(ctx context.Context) (string, error) {
	if err := r.isRepo(ctx); err != nil {
		return "", err
	}
	if b, err := r.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil && b != "" {
		return b, nil
	}
	sha, err := r.git(ctx, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return "(detached " + sha + ")", nil
}

// List lists the local branches and those only origin has, as of the last
// fetch, newest commit first. Nothing is fetched.
func (r Repo) List(ctx context.Context) (Listing, error) {
	if err := r.isRepo(ctx); err != nil {
		return Listing{}, err
	}
	var l Listing
	var err error
	if l.Root, err = r.git(ctx, "rev-parse", "--show-toplevel"); err != nil {
		return l, err
	}
	l.Root = filepath.FromSlash(l.Root)
	current, _ := r.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if current == "" {
		l.Detached, _ = r.git(ctx, "rev-parse", "--short", "HEAD")
	}
	out, err := r.git(ctx, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname)%09%(committerdate:unix)%09%(symref)%09%(worktreepath)",
		"refs/heads", "refs/remotes/"+remote)
	if err != nil {
		return l, err
	}
	l.Branches = parseRefs(out, current, l.Root)
	return l, nil
}

// parseRefs reads List's for-each-ref lines, in their order. A remote
// branch with a local one of the same name is left out, as is origin/HEAD.
// root is this checkout's top, whose branch has no Worktree.
func parseRefs(out, current, root string) []Branch {
	local := map[string]bool{}
	type ref struct {
		Branch
		remote bool
	}
	var refs []ref
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 || f[2] != "" { // a symref, such as origin/HEAD
			continue
		}
		var b Branch
		if secs, err := strconv.ParseInt(f[1], 10, 64); err == nil {
			b.Date = time.Unix(secs, 0)
		}
		switch name := f[0]; {
		case strings.HasPrefix(name, "refs/heads/"):
			b.Name = strings.TrimPrefix(name, "refs/heads/")
			b.Current = b.Name == current
			if wt := filepath.FromSlash(f[3]); wt != "" && !b.Current && !samePath(wt, root) {
				b.Worktree = wt
			}
			local[b.Name] = true
			refs = append(refs, ref{Branch: b})
		case strings.HasPrefix(name, "refs/remotes/"+remote+"/"):
			b.Name, b.Remote = strings.TrimPrefix(name, "refs/remotes/"+remote+"/"), true
			refs = append(refs, ref{Branch: b, remote: true})
		}
	}
	origin := map[string]time.Time{}
	for _, r := range refs {
		if r.remote {
			origin[r.Name] = r.Date
		}
	}
	var branches []Branch
	for _, r := range refs {
		if !r.remote || !local[r.Name] {
			r.OriginDate = origin[r.Name]
			branches = append(branches, r.Branch)
		}
	}
	return branches
}

// samePath reports whether a and b are one folder, through symlinks
// (macOS's /var is /private/var).
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// Switch checks out the branch name of l: `git switch name`, or for a
// branch only origin has, `git switch --track origin/name`, which makes
// the local branch. Only a branch List lists can be switched to. When git
// refuses, as it does when uncommitted changes would be overwritten, the
// error is git's message.
func (r Repo) Switch(ctx context.Context, name string) error {
	l, err := r.List(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(l.Branches, func(b Branch) bool { return b.Name == name })
	if i < 0 {
		return fmt.Errorf("no branch %s", name)
	}
	if l.Branches[i].Current {
		return nil
	}
	args := []string{"switch", name}
	if l.Branches[i].Remote {
		args = []string{"switch", "--track", remote + "/" + name}
	}
	out, err := cmdrun.Git(ctx, r.Dir, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("git switch: %w", err)
	}
	return nil
}

// Fetch runs `git fetch --prune origin`. git is told never to ask for a
// password or passphrase, which nobody would see: a remote that needs one
// fails with git's message.
func (r Repo) Fetch(ctx context.Context) error {
	if err := r.isRepo(ctx); err != nil {
		return err
	}
	cmd := cmdrun.Git(ctx, r.Dir, "fetch", "--prune", remote)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("git fetch: %w", err)
	}
	return nil
}

// watched are the files in the git directory whose change can change the
// branch or how it compares with origin's: HEAD (a switch), FETCH_HEAD (a
// fetch), ORIG_HEAD (a reset, merge or rebase) and COMMIT_EDITMSG (a
// commit). The index is left out: reading the status rewrites it.
var watched = []string{"HEAD", "FETCH_HEAD", "ORIG_HEAD", "COMMIT_EDITMSG"}

// Watch reports changes to the checkout's HEAD and the other watched
// files, debounced into one notification. The channel is closed when ctx
// is done.
func (r Repo) Watch(ctx context.Context, debounce time.Duration) (<-chan struct{}, error) {
	if err := r.isRepo(ctx); err != nil {
		return nil, err
	}
	files := map[string]bool{}
	dirs := map[string]bool{}
	for _, name := range watched {
		p, err := r.git(ctx, "rev-parse", "--git-path", name)
		if err != nil {
			return nil, err
		}
		if p = filepath.FromSlash(p); !filepath.IsAbs(p) { // relative to Dir
			p = filepath.Join(r.Dir, p)
		}
		p = filepath.Clean(p)
		files[p] = true
		dirs[filepath.Dir(p)] = true
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	for d := range dirs {
		if err := w.Add(d); err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		defer func() { _ = w.Close() }()
		timer := time.NewTimer(debounce)
		timer.Stop()
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if ev.Op == fsnotify.Chmod {
					continue
				}
				// git writes HEAD.lock and renames it to HEAD.
				if files[filepath.Clean(ev.Name)] || files[strings.TrimSuffix(filepath.Clean(ev.Name), ".lock")] {
					timer.Reset(debounce)
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			case <-timer.C:
				select {
				case out <- struct{}{}:
				default: // a notification is already pending
				}
			}
		}
	}()
	return out, nil
}

// Age is how long ago t was, briefly: "now", "5m", "3h", "2d", "6w",
// "4mo", "2y".
func Age(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case t.IsZero():
		return ""
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d < 14*24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	case d < 60*24*time.Hour:
		return strconv.Itoa(int(d/(7*24*time.Hour))) + "w"
	case d < 365*24*time.Hour:
		return strconv.Itoa(int(d/(30*24*time.Hour))) + "mo"
	}
	return strconv.Itoa(int(d/(365*24*time.Hour))) + "y"
}

package changes

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// ErrNotGit means the project isn't in a git repository, so it has no
// Changed Actions.
var ErrNotGit = errors.New("the project is not in a git repository")

// ErrNotPushed means an API run's changes were asked for on a branch that
// origin doesn't have (as of the last fetch): Dataform compiles the pushed
// branch, so there is nothing to compare yet.
var ErrNotPushed = errors.New("the current branch is not on origin: API runs compile the pushed branch, so push it first")

// ErrDetached means an API run's changes were asked for on a detached HEAD:
// API runs compile a branch.
var ErrDetached = errors.New("HEAD is detached: API runs compile a pushed branch, so check one out")

// Base is what a diff compares: the merge-base of the default branch and
// the head.
type Base struct {
	// BaseRef is origin/<DefaultBranch> when it exists, else the local
	// branch.
	BaseRef       string
	MergeBase     string // commit SHA
	DefaultBranch string
	// OnDefaultBranch is set when the checked-out branch is the default one.
	OnDefaultBranch bool
	// HeadRef is the branch compared (origin/<branch> for HeadPushed), or
	// a short SHA on a detached HEAD.
	HeadRef string
	// HeadSHA is origin/<branch>'s commit for HeadPushed; "" for the
	// working tree.
	HeadSHA string
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := cmdrun.Git(ctx, dir, args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveBaseRef is origin/<branch> when it exists, else the local branch.
// Nothing is fetched.
func resolveBaseRef(ctx context.Context, dir, branch string) (string, error) {
	for _, ref := range []string{"origin/" + branch, branch} {
		if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("neither origin/%s nor %s exists: set defaultBranch (--default-branch) to your default branch", branch, branch)
}

// ResolveBase works out what to compare for the project in dir. The working
// tree (HeadWorktree) is compared with the merge-base of the default branch
// and HEAD; the pushed commit (HeadPushed), which is what an API run
// compiles, with the merge-base of the default branch and it.
func ResolveBase(ctx context.Context, dir, branch string, head Head) (Base, error) {
	if out, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil || out != "true" {
		if ctx.Err() != nil {
			return Base{}, ctx.Err()
		}
		return Base{}, ErrNotGit
	}
	b := Base{DefaultBranch: branch}
	var err error
	if b.BaseRef, err = resolveBaseRef(ctx, dir, branch); err != nil {
		return b, err
	}
	// A detached HEAD reports "HEAD", so it counts as a feature branch.
	current, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return b, err
	}
	b.OnDefaultBranch = current == branch
	b.HeadRef = current
	if current == "HEAD" {
		if b.HeadRef, err = git(ctx, dir, "rev-parse", "--short", "HEAD"); err != nil {
			return b, err
		}
	}
	rev := "HEAD"
	if head == HeadPushed {
		// An API run compiles the branch of the same name on origin (see
		// dataformapi), whatever the local branch tracks.
		if current == "HEAD" {
			return b, ErrDetached
		}
		if b.HeadSHA, err = git(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+current+"^{commit}"); err != nil {
			if ctx.Err() != nil {
				return b, ctx.Err()
			}
			return b, ErrNotPushed
		}
		b.HeadRef = "origin/" + current
		rev = b.HeadSHA
	}
	if b.MergeBase, err = git(ctx, dir, "merge-base", b.BaseRef, rev); err != nil {
		if ctx.Err() != nil {
			return b, ctx.Err()
		}
		return b, fmt.Errorf("no common ancestor of %s and %s: %w", b.BaseRef, b.HeadRef, err)
	}
	return b, nil
}

// ChangedFiles lists the files under dir, relative to it and with forward
// slashes, that differ from the merge-base of branch and HEAD or that git
// doesn't track yet. Without that merge-base (no such branch) they are the
// files that differ from HEAD.
func ChangedFiles(ctx context.Context, dir, branch string) ([]string, error) {
	rev := "HEAD"
	if b, err := ResolveBase(ctx, dir, branch, HeadWorktree); err == nil {
		rev = b.MergeBase
	} else if errors.Is(err, ErrNotGit) || ctx.Err() != nil {
		return nil, err
	}
	var files []string
	for _, args := range [][]string{
		{"diff", "--name-only", "--relative", "-z", rev, "--", "."},
		{"ls-files", "--others", "--exclude-standard", "-z", "--", "."},
	} {
		out, err := git(ctx, dir, args...)
		if err != nil {
			return nil, err
		}
		for _, f := range strings.Split(out, "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
	}
	return files, nil
}

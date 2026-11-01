package dataformapi

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// GitState is what an API run needs to know about the local checkout: the
// branch Dataform compiles, and how far what is on disk is from what was
// pushed. Remote branches are as of the last fetch; nothing is fetched.
type GitState struct {
	Branch       string // "" when HEAD is detached
	SHA          string // HEAD's short SHA when it is detached
	Origin       string // URL of the origin remote, "" when there is none
	RemoteBranch bool   // origin/<Branch> exists
	Ahead        int    // local commits not on origin/<Branch>
	Behind       int    // commits on origin/<Branch> not checked out
	Dirty        []string
	// Unpushed and Unpulled are the first maxCommits of those commits,
	// newest first, as "abc1234 subject".
	Unpushed, Unpulled []string
}

// maxCommits is how many unpushed and unpulled commits ReadGit lists.
const maxCommits = 20

// ReadGit reads the state of the checkout that holds dir. Only uncommitted
// files under dir count as dirty.
func ReadGit(ctx context.Context, dir string) (GitState, error) {
	git := func(args ...string) (string, error) {
		out, err := cmdrun.Git(ctx, dir, args...).Output()
		return strings.TrimRight(string(out), "\n"), err
	}
	var g GitState
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		return g, fmt.Errorf("%s is not in a git repository: API runs compile a pushed branch", dir)
	}
	g.Branch, _ = git("symbolic-ref", "--quiet", "--short", "HEAD")
	if g.Branch == "" {
		g.SHA, _ = git("rev-parse", "--short", "HEAD")
	}
	g.Origin, _ = git("remote", "get-url", "origin")
	if g.Branch != "" {
		if _, err := git("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+g.Branch); err == nil {
			g.RemoteBranch = true
			counts, err := git("rev-list", "--left-right", "--count", "HEAD...refs/remotes/origin/"+g.Branch)
			if err != nil {
				return g, fmt.Errorf("compare with origin/%s: %w", g.Branch, err)
			}
			g.Ahead, g.Behind = parseAheadBehind(counts)
			list := func(revs string) []string {
				out, _ := git("log", "--format=%h %s", "-n", strconv.Itoa(maxCommits), revs)
				if out == "" {
					return nil
				}
				return strings.Split(out, "\n")
			}
			if g.Ahead > 0 {
				g.Unpushed = list("refs/remotes/origin/" + g.Branch + "..HEAD")
			}
			if g.Behind > 0 {
				g.Unpulled = list("HEAD..refs/remotes/origin/" + g.Branch)
			}
		}
	}
	status, err := git("status", "--porcelain", "--", ".")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return g, fmt.Errorf("git status: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return g, fmt.Errorf("git status: %w", err)
	}
	g.Dirty = parsePorcelain(status)
	return g, nil
}

// parseAheadBehind parses `git rev-list --left-right --count A...B`.
func parseAheadBehind(s string) (ahead, behind int) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, 0
	}
	ahead, _ = strconv.Atoi(f[0])
	behind, _ = strconv.Atoi(f[1])
	return ahead, behind
}

// parsePorcelain returns the paths `git status --porcelain` lists (the new
// path of a rename).
func parsePorcelain(s string) []string {
	var paths []string
	for _, line := range strings.Split(s, "\n") {
		if len(line) < 4 {
			continue
		}
		p := line[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		paths = append(paths, strings.Trim(p, `"`))
	}
	return paths
}

// RepoNameFromURL is the Dataform repository name xf assumes for a git
// remote: the last segment of its path without ".git", as the extension
// takes it. It is "" when the URL has no path.
func RepoNameFromURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")
	i := strings.LastIndexAny(u, "/:")
	if i < 0 || i == len(u)-1 {
		return ""
	}
	return u[i+1:]
}

// Blocker says why an API run can't start from this checkout: Dataform
// compiles origin/<Branch>, so there must be one. It is "" when a run can
// start.
func (g GitState) Blocker() string {
	switch {
	case g.Branch == "":
		return "HEAD is detached: API runs compile a pushed branch, so check one out"
	case !g.RemoteBranch:
		return "origin/" + g.Branch + " doesn't exist (as of the last fetch): push the branch first"
	}
	return ""
}

// Warnings say how what runs in GCP (origin/<branch>) differs from the
// checkout. They are empty when the two match, and say nothing of a
// Blocker.
func (g GitState) Warnings() []string {
	var w []string
	remote := "origin/" + g.Branch
	if g.Ahead > 0 {
		w = append(w, fmt.Sprintf("%d local commit%s not pushed to %s won't run", g.Ahead, plural(g.Ahead), remote))
	}
	if g.Behind > 0 {
		w = append(w, fmt.Sprintf("%s has %d commit%s you haven't pulled; they will run", remote, g.Behind, plural(g.Behind)))
	}
	if n := len(g.Dirty); n > 0 {
		const show = 3
		names := strings.Join(g.Dirty[:min(n, show)], ", ")
		if n > show {
			names += fmt.Sprintf(" and %d more", n-show)
		}
		w = append(w, fmt.Sprintf("%d uncommitted file%s won't run: %s", n, plural(n), names))
	}
	return w
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

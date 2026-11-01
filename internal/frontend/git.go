package frontend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/sahilm/fuzzy"
)

// maxGitDetail is how many files or commits a part lists for a hover.
const maxGitDetail = 50

// APIGitLine is the git line for checkout g, or for err reading it. verb
// is what the view does with the pushed branch: "run" (Run pane, R, T) or
// "check" (I, C). known is false until the checkout has been read.
func APIGitLine(g dataformapi.GitState, err error, known bool, verb string) []GitPart {
	does, wont, will := "runs ", " won't run", " will run"
	if verb == "check" {
		does, wont, will = "checks ", " won't be checked", " will be checked"
	}
	switch {
	case !known:
		return []GitPart{{Text: does + "the pushed branch", Tone: "dim"}}
	case err != nil:
		return []GitPart{{Text: err.Error(), Tone: "error"}}
	case g.Blocker() != "":
		return []GitPart{{Text: g.Blocker(), Tone: "error"}}
	}
	sep := GitPart{Text: " · ", Tone: "dim"}
	parts := []GitPart{{Text: does, Tone: "dim"}, {Text: "origin/" + g.Branch}}
	var lost []GitPart
	if n := len(g.Dirty); n > 0 {
		lost = append(lost, GitPart{Text: fmt.Sprintf("%d uncommitted file%s", n, Plural(n)), Tone: "warn", Detail: gitDetail(g.Dirty, n)})
	}
	if n := g.Ahead; n > 0 {
		lost = append(lost, GitPart{Text: fmt.Sprintf("%d unpushed commit%s", n, Plural(n)), Tone: "warn", Detail: gitDetail(g.Unpushed, n)})
	}
	if len(lost) > 0 {
		parts = append(parts, sep)
		for i, p := range lost {
			if i > 0 {
				parts = append(parts, GitPart{Text: ", ", Tone: "warn"})
			}
			parts = append(parts, p)
		}
		parts = append(parts, GitPart{Text: wont, Tone: "warn"})
	}
	if n := g.Behind; n > 0 {
		parts = append(parts, sep,
			GitPart{Text: fmt.Sprintf("%d unpulled commit%s", n, Plural(n)), Tone: "warn", Detail: gitDetail(g.Unpulled, n)},
			GitPart{Text: will, Tone: "warn"})
	}
	return parts
}

// gitDetail is up to maxGitDetail of items, of n in all, and how many
// more there are.
func gitDetail(items []string, n int) []string {
	out := append([]string{}, items[:min(len(items), maxGitDetail)]...)
	if more := n - len(out); more > 0 {
		out = append(out, fmt.Sprintf("and %d more", more))
	}
	return out
}

// BranchParts is the status line's branch for checkout g, or nothing
// before it is read and outside a repository (err).
func BranchParts(g dataformapi.GitState, err error, known bool) []GitPart {
	if !known || err != nil {
		return nil
	}
	if g.Branch == "" {
		return []GitPart{{Text: "⎇ (detached " + g.SHA + ")", Tone: "warn"}}
	}
	parts := []GitPart{{Text: "⎇ " + g.Branch}}
	if n := len(g.Dirty); n > 0 {
		parts = append(parts, GitPart{Text: fmt.Sprintf(" ●%d", n), Tone: "warn",
			Detail: append([]string{fmt.Sprintf("%d uncommitted file%s", n, Plural(n))}, gitDetail(g.Dirty, n)...)})
	}
	if n := g.Ahead; n > 0 {
		parts = append(parts, GitPart{Text: fmt.Sprintf(" ↑%d", n),
			Detail: append([]string{fmt.Sprintf("%d commit%s not pushed to origin/%s", n, Plural(n), g.Branch)}, gitDetail(g.Unpushed, n)...)})
	}
	if n := g.Behind; n > 0 {
		parts = append(parts, GitPart{Text: fmt.Sprintf(" ↓%d", n),
			Detail: append([]string{fmt.Sprintf("%d commit%s on origin/%s not pulled", n, Plural(n), g.Branch)}, gitDetail(g.Unpulled, n)...)})
	}
	return parts
}

// FindBranches lists the branches query fuzzy-matches, best first; all of
// them, as listed, for an empty query.
func FindBranches(all []gitbranch.Branch, query string) []gitbranch.Branch {
	q := strings.TrimSpace(query)
	if q == "" {
		return all
	}
	names := make([]string, len(all))
	for i, b := range all {
		names[i] = b.Name
	}
	var out []gitbranch.Branch
	for _, m := range fuzzy.Find(q, names) {
		out = append(out, all[m.Index])
	}
	return out
}

// BranchRootNote says that a switch changes the whole checkout at
// repoRoot, when that holds more than the project at root; "" when the
// project is the checkout.
func BranchRootNote(root, repoRoot string) string {
	if repoRoot == "" || project.RealPath(filepath.Clean(root)) == project.RealPath(filepath.Clean(repoRoot)) {
		return ""
	}
	home, _ := os.UserHomeDir()
	return "switches the whole checkout at " + ShortPath(home, repoRoot)
}

// BaseBranches are the branches R, I and C can compare with: all but the
// checked-out one, which would compare with itself, the current base
// first, then the others as listed (newest commit first).
func BaseBranches(all []gitbranch.Branch, base string) []gitbranch.Branch {
	var first, rest []gitbranch.Branch
	for _, b := range all {
		switch {
		case b.Current:
		case b.Name == base:
			first = append(first, b)
		default:
			rest = append(rest, b)
		}
	}
	return append(first, rest...)
}

// BaseTags are a base branch's tags: "base" for the one compared with now,
// "local only" when origin has no such branch, and the age of the tip
// compared with, origin's when there is one.
func BaseTags(b gitbranch.Branch, base string, now time.Time) string {
	var tags []string
	if b.Name == base {
		tags = append(tags, "base")
	}
	date := b.OriginDate
	if date.IsZero() {
		date = b.Date
		if !b.Remote {
			tags = append(tags, "local only")
		}
	}
	if age := gitbranch.Age(now, date); age != "" {
		tags = append(tags, age)
	}
	return strings.Join(tags, " · ")
}

// SaveBase makes name the base in the project's config file with save
// (Deps.SaveBranch), and says so as the footer does; nil save, or a
// failed one, leaves it for this session.
func SaveBase(save func(string) error, name string) string {
	if save == nil {
		return "comparing with " + name + " until xf restarts: --config reads another file"
	}
	if err := save(name); err != nil {
		return "comparing with " + name + " until xf restarts: not saved: " + err.Error()
	}
	return "comparing with " + name + ", saved for this project"
}

// BranchRunBlocker is why a branch can't be switched while a local run is
// in progress.
const BranchRunBlocker = "a run is in progress: a switch would change its files as it runs (ctrl+c cancels it)"

// BranchSwitched is what the footer says after a switch to name.
func BranchSwitched(name string) string { return "switched to " + name }

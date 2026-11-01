package gitbranch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitIn runs git in dir with a fixed identity and no global config.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitAt commits file with body, dated at secs.
func commitAt(t *testing.T, dir, file, body string, secs int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", file)
	date := time.Unix(secs, 0).Format(time.RFC3339)
	cmd := exec.Command("git", "commit", "-q", "-m", file)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
}

// fixture is a clone of an origin with main, old and colleague; the clone
// has main, old and its own local, and the project is in a subfolder.
func fixture(t *testing.T) (clone, project string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	if err := os.Mkdir(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "init", "-q", "-b", "main")
	commitAt(t, origin, "proj/a.sqlx", "a", 1000)
	gitIn(t, origin, "branch", "old")
	gitIn(t, origin, "switch", "-q", "-c", "colleague")
	commitAt(t, origin, "proj/c.sqlx", "c", 3000)
	gitIn(t, origin, "switch", "-q", "main")
	clone = filepath.Join(base, "clone")
	gitIn(t, base, "clone", "-q", origin, clone)
	gitIn(t, clone, "branch", "old", "origin/old")
	gitIn(t, clone, "switch", "-q", "-c", "local")
	commitAt(t, clone, "proj/l.sqlx", "l", 2000)
	gitIn(t, clone, "switch", "-q", "main")
	return clone, filepath.Join(clone, "proj")
}

func names(bs []Branch) []string {
	var out []string
	for _, b := range bs {
		s := b.Name
		if b.Remote {
			s += " (remote)"
		}
		if b.Current {
			s += " *"
		}
		out = append(out, s)
	}
	return out
}

func TestList(t *testing.T) {
	clone, project := fixture(t)
	r := Repo{Dir: project}
	l, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Newest first; origin/main and origin/old have local branches, and
	// origin/HEAD is a symref.
	got := strings.Join(names(l.Branches), ", ")
	if want := "colleague (remote), local, main *, old"; got != want { // main and old tie, by name
		t.Errorf("branches = %s, want %s", got, want)
	}
	if real, _ := filepath.EvalSymlinks(clone); l.Root != clone && l.Root != real {
		t.Errorf("Root = %s, want %s", l.Root, clone)
	}
	if l.Detached != "" {
		t.Errorf("Detached = %q on a branch", l.Detached)
	}
	if b := l.Branches[0]; b.Date.Unix() != 3000 {
		t.Errorf("colleague's date = %d, want 3000", b.Date.Unix())
	}
	if cur, err := r.Current(context.Background()); err != nil || cur != "main" {
		t.Errorf("Current = %q, %v", cur, err)
	}
}

func TestListWorktree(t *testing.T) {
	clone, project := fixture(t)
	other := filepath.Join(filepath.Dir(clone), "wt")
	gitIn(t, clone, "worktree", "add", "-q", other, "local")
	l, err := Repo{Dir: project}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range l.Branches {
		want := ""
		if b.Name == "local" {
			want = other
		}
		if b.Worktree != want && !samePath(b.Worktree, want) {
			t.Errorf("%s's worktree = %q, want %q", b.Name, b.Worktree, want)
		}
	}
	// From the worktree, main is in the clone and local is its own.
	l, err = Repo{Dir: filepath.Join(other, "proj")}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range l.Branches {
		if b.Name == "main" && !samePath(b.Worktree, clone) || b.Name == "local" && b.Worktree != "" {
			t.Errorf("from the worktree, %s's worktree = %q", b.Name, b.Worktree)
		}
	}
}

func TestSwitch(t *testing.T) {
	clone, project := fixture(t)
	r := Repo{Dir: project}
	ctx := context.Background()

	if err := r.Switch(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	if b := gitIn(t, clone, "branch", "--show-current"); b != "local" {
		t.Errorf("after switch, on %s", b)
	}

	// A remote-only branch gets a local branch that tracks it.
	if err := r.Switch(ctx, "colleague"); err != nil {
		t.Fatal(err)
	}
	if up := gitIn(t, clone, "rev-parse", "--abbrev-ref", "colleague@{upstream}"); up != "origin/colleague" {
		t.Errorf("colleague tracks %s", up)
	}
	l, _ := r.List(ctx)
	if got := strings.Join(names(l.Branches), ", "); strings.Contains(got, "remote") || !strings.Contains(got, "colleague *") {
		t.Errorf("after the remote switch: %s", got)
	}

	if err := r.Switch(ctx, "nope"); err == nil || err.Error() != "no branch nope" {
		t.Errorf("Switch(nope) = %v", err)
	}
	if err := r.Switch(ctx, "colleague"); err != nil {
		t.Errorf("Switch to the current branch = %v", err)
	}
}

// TestSwitchRefused: an uncommitted change that the other branch would
// overwrite is kept, and git's message is the error.
func TestSwitchRefused(t *testing.T) {
	clone, project := fixture(t)
	r := Repo{Dir: project}
	ctx := context.Background()
	gitIn(t, clone, "switch", "-q", "local")
	if err := os.WriteFile(filepath.Join(project, "l.sqlx"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := r.Switch(ctx, "main")
	if err == nil || !strings.Contains(err.Error(), "l.sqlx") {
		t.Fatalf("Switch = %v, want git's refusal naming l.sqlx", err)
	}
	if b := gitIn(t, clone, "branch", "--show-current"); b != "local" {
		t.Errorf("on %s after a refused switch", b)
	}
	if body, _ := os.ReadFile(filepath.Join(project, "l.sqlx")); string(body) != "edited" {
		t.Errorf("the edit was lost: %q", body)
	}

	// A change git can carry over is carried over.
	if err := os.WriteFile(filepath.Join(project, "a.sqlx"), []byte("carried"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "checkout", "--", "proj/l.sqlx")
	if err := r.Switch(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(project, "a.sqlx")); string(body) != "carried" {
		t.Errorf("a.sqlx = %q after the switch", body)
	}
}

func TestDetached(t *testing.T) {
	clone, project := fixture(t)
	gitIn(t, clone, "switch", "-q", "--detach", "HEAD")
	sha := gitIn(t, clone, "rev-parse", "--short", "HEAD")
	r := Repo{Dir: project}
	l, err := r.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if l.Detached != sha {
		t.Errorf("Detached = %q, want %q", l.Detached, sha)
	}
	for _, b := range l.Branches {
		if b.Current {
			t.Errorf("%s is current on a detached HEAD", b.Name)
		}
	}
	if cur, _ := r.Current(context.Background()); cur != "(detached "+sha+")" {
		t.Errorf("Current = %q", cur)
	}
}

func TestNotGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	r := Repo{Dir: dir}
	if _, err := r.List(context.Background()); !errors.Is(err, ErrNotGit) {
		t.Errorf("List = %v, want ErrNotGit", err)
	}
	if _, err := r.Current(context.Background()); !errors.Is(err, ErrNotGit) {
		t.Errorf("Current = %v, want ErrNotGit", err)
	}
	if err := r.Switch(context.Background(), "main"); !errors.Is(err, ErrNotGit) {
		t.Errorf("Switch = %v, want ErrNotGit", err)
	}
}

func TestFetch(t *testing.T) {
	clone, project := fixture(t)
	origin := filepath.Join(filepath.Dir(clone), "origin")
	gitIn(t, origin, "branch", "pushed-later")
	r := Repo{Dir: project}
	ctx := context.Background()
	if err := r.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	l, _ := r.List(ctx)
	if got := strings.Join(names(l.Branches), ", "); !strings.Contains(got, "pushed-later (remote)") {
		t.Errorf("after fetch: %s", got)
	}
	gitIn(t, origin, "branch", "-D", "pushed-later")
	if err := r.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	l, _ = r.List(ctx)
	if got := strings.Join(names(l.Branches), ", "); strings.Contains(got, "pushed-later") {
		t.Errorf("a deleted branch is kept after fetch --prune: %s", got)
	}
}

func TestWatch(t *testing.T) {
	clone, project := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Repo{Dir: project}.Watch(ctx, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "switch", "-q", "local")
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no notification after a switch")
	}
	cancel()
	for range ch { // closed when ctx is done
	}
}

func TestAge(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	for _, tt := range []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "now"}, {5 * time.Minute, "5m"}, {3 * time.Hour, "3h"}, {2 * 24 * time.Hour, "2d"},
		{20 * 24 * time.Hour, "2w"}, {100 * 24 * time.Hour, "3mo"}, {800 * 24 * time.Hour, "2y"},
	} {
		if got := Age(now, now.Add(-tt.ago)); got != tt.want {
			t.Errorf("Age(%v) = %q, want %q", tt.ago, got, tt.want)
		}
	}
	if Age(now, time.Time{}) != "" {
		t.Error("Age of the zero time")
	}
}

package gitscm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// isolate gives git a fixed identity and no config of the user's, for
// the test's commands and Repo's.
func isolate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t",
		"GIT_COMMITTER_EMAIL": "t@t", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1"} {
		t.Setenv(k, v)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, file, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture is a clone of a bare origin, with one commit pushed, and the
// repository opened at its sub folder.
func fixture(t *testing.T) (clone, origin string, r *Repo) {
	t.Helper()
	isolate(t)
	base := t.TempDir()
	origin = filepath.Join(base, "origin.git")
	gitIn(t, base, "init", "-q", "--bare", "-b", "main", origin)
	clone = filepath.Join(base, "clone")
	gitIn(t, base, "clone", "-q", origin, clone)
	gitIn(t, clone, "switch", "-q", "-c", "main")
	write(t, clone, "sub/a.sqlx", "one\ntwo\nthree\n")
	write(t, clone, "top.txt", "top\n")
	gitIn(t, clone, "add", "-A")
	gitIn(t, clone, "commit", "-q", "-m", "first")
	gitIn(t, clone, "push", "-q", "-u", "origin", "main")
	var err error
	if r, err = Open(context.Background(), filepath.Join(clone, "sub")); err != nil {
		t.Fatal(err)
	}
	return clone, origin, r
}

func paths(cs []Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Kind+" "+c.Path)
	}
	return out
}

func TestOpenAndStatus(t *testing.T) {
	clone, _, r := fixture(t)
	ctx := context.Background()
	if r.Prefix != "sub/" {
		t.Errorf("prefix %q", r.Prefix)
	}
	write(t, clone, "sub/a.sqlx", "one\n2\nthree\n")
	write(t, clone, "sub/new file.js", "x\n")
	gitIn(t, clone, "rm", "-q", "top.txt")
	st, err := r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" || st.Upstream != "origin/main" || st.Head == "" || st.Op != "" {
		t.Errorf("branch %+v", st)
	}
	if got := paths(st.Staged); !slices.Equal(got, []string{"D top.txt"}) {
		t.Errorf("staged %v", got)
	}
	if got := paths(st.Changes); !slices.Equal(got, []string{"M sub/a.sqlx", "U sub/new file.js"}) {
		t.Errorf("changes %v", got)
	}
	if _, err := Open(ctx, t.TempDir()); !errors.Is(err, ErrNotGit) {
		t.Errorf("outside a repository: %v", err)
	}
}

func TestStageUnstageAndShow(t *testing.T) {
	clone, _, r := fixture(t)
	ctx := context.Background()
	write(t, clone, "sub/a.sqlx", "one\n2\nthree\n")
	if err := r.Stage(ctx, "sub/a.sqlx"); err != nil {
		t.Fatal(err)
	}
	b, err := r.Show(ctx, "", "sub/a.sqlx", 1<<20)
	if err != nil || b.Text != "one\n2\nthree\n" {
		t.Fatalf("index %+v %v", b, err)
	}
	if b, _ := r.Show(ctx, "HEAD", "sub/a.sqlx", 1<<20); b.Text != "one\ntwo\nthree\n" {
		t.Errorf("HEAD %+v", b)
	}
	if b, _ := r.Show(ctx, "HEAD", "sub/none", 1<<20); b.Exists {
		t.Errorf("a file HEAD hasn't: %+v", b)
	}
	if err := r.Unstage(ctx, "sub/a.sqlx"); err != nil {
		t.Fatal(err)
	}
	st, _ := r.Status(ctx)
	if len(st.Staged) != 0 || len(st.Changes) != 1 {
		t.Errorf("after unstage %+v", st)
	}
	if _, err := r.Show(ctx, "", "../etc/passwd", 10); err == nil {
		t.Error("a path out of the repository was read")
	}
	if err := r.StageAll(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(ctx); len(st.Staged) != 1 {
		t.Errorf("stage all %+v", st)
	}
	if err := r.UnstageAll(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(ctx); len(st.Staged) != 0 {
		t.Errorf("unstage all %+v", st)
	}
}

func TestSetIndex(t *testing.T) {
	clone, _, r := fixture(t)
	ctx := context.Background()
	was := "one\ntwo\nthree\n"
	if err := r.SetIndex(ctx, "sub/a.sqlx", "one\nTWO\nthree\n", &was); err != nil {
		t.Fatal(err)
	}
	if b, _ := r.Show(ctx, "", "sub/a.sqlx", 1<<20); b.Text != "one\nTWO\nthree\n" {
		t.Errorf("index %q", b.Text)
	}
	// The page worked from the old index: refused.
	if err := r.SetIndex(ctx, "sub/a.sqlx", "x\n", &was); !errors.Is(err, ErrIndexChanged) {
		t.Errorf("stale: %v", err)
	}
	// A file new to the index.
	write(t, clone, "sub/n.sql", "a\r\nb\r\n")
	if err := r.SetIndex(ctx, "sub/n.sql", "a\n", nil); err != nil {
		t.Fatal(err)
	}
	// The file's CRLF line ends are kept.
	out, _ := exec.Command("git", "-C", clone, "cat-file", "-p", ":sub/n.sql").Output()
	if string(out) != "a\r\n" {
		t.Errorf("crlf: %q", out)
	}
}

func TestCommitAmendAndLog(t *testing.T) {
	clone, _, r := fixture(t)
	ctx := context.Background()
	if !r.Pushed(ctx) {
		t.Error("the pushed commit isn't pushed")
	}
	write(t, clone, "sub/a.sqlx", "changed\n")
	_ = r.Stage(ctx, "sub/a.sqlx")
	if _, err := r.Commit(ctx, "second\n\nbody", false); err != nil {
		t.Fatal(err)
	}
	if r.Pushed(ctx) {
		t.Error("an unpushed commit is pushed")
	}
	if m, _ := r.HeadMessage(ctx); m != "second\n\nbody" {
		t.Errorf("message %q", m)
	}
	if _, err := r.Commit(ctx, "second, amended", true); err != nil {
		t.Fatal(err)
	}
	log, err := r.Log(ctx, 0, 50)
	if err != nil || len(log) != 2 || log[0].Subject != "second, amended" || !log[0].Unpushed || log[1].Unpushed {
		t.Fatalf("log %+v %v", log, err)
	}
	files, parent, err := r.CommitFiles(ctx, log[0].SHA)
	if err != nil || parent != log[1].SHA || !slices.Equal(paths(files), []string{"M sub/a.sqlx"}) {
		t.Errorf("files %v %q %v", files, parent, err)
	}
	files, parent, _ = r.CommitFiles(ctx, log[1].SHA)
	if parent != "" || len(files) != 2 {
		t.Errorf("first commit's files %v %q", files, parent)
	}
	if more, _ := r.Log(ctx, 1, 50); len(more) != 1 {
		t.Errorf("skip %+v", more)
	}
	if _, err := r.Commit(ctx, "  ", false); err == nil {
		t.Error("an empty message committed")
	}
}

func TestPushPull(t *testing.T) {
	clone, origin, r := fixture(t)
	ctx := context.Background()
	var log []Entry
	r.OnWrite = func(e Entry) { log = append(log, e) }
	write(t, clone, "sub/a.sqlx", "pushed\n")
	_ = r.Stage(ctx, "sub/a.sqlx")
	_, _ = r.Commit(ctx, "to push", false)
	st, _ := r.Status(ctx)
	if st.Ahead != 1 {
		t.Errorf("ahead %d", st.Ahead)
	}
	if _, err := r.Push(ctx, st); err != nil {
		t.Fatal(err)
	}
	// A new branch with no upstream is pushed to origin and tracks it.
	if err := r.NewBranch(ctx, "feature", ""); err != nil {
		t.Fatal(err)
	}
	st, _ = r.Status(ctx)
	if st.Branch != "feature" || st.Upstream != "" {
		t.Fatalf("new branch %+v", st)
	}
	if _, err := r.Push(ctx, st); err != nil {
		t.Fatal(err)
	}
	if st, _ = r.Status(ctx); st.Upstream != "origin/feature" {
		t.Errorf("upstream %q", st.Upstream)
	}
	// Someone else pushes to feature: pull moves forward.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", "-b", "feature", origin, other)
	write(t, other, "theirs.txt", "x\n")
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "theirs")
	gitIn(t, other, "push", "-q")
	if _, err := r.Pull(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(clone, "theirs.txt")); err != nil {
		t.Error("pull didn't bring their file")
	}
	// Both moved on: refused.
	write(t, other, "theirs.txt", "y\n")
	gitIn(t, other, "commit", "-q", "-am", "theirs 2")
	gitIn(t, other, "push", "-q")
	write(t, clone, "mine.txt", "m\n")
	_ = r.StageAll(ctx)
	_, _ = r.Commit(ctx, "mine", false)
	if _, err := r.Pull(ctx); err == nil {
		t.Error("a pull that can't fast-forward worked")
	}
	if len(log) == 0 || log[len(log)-1].OK || log[len(log)-1].Args[1] != "pull" {
		t.Errorf("log %+v", log[len(log)-1])
	}
	if _, err := r.Push(ctx, Status{}); err == nil {
		t.Error("a detached push worked")
	}
	if err := r.NewBranch(ctx, "bad name", ""); err == nil {
		t.Error("a bad branch name was made")
	}
}

func TestStashes(t *testing.T) {
	clone, _, r := fixture(t)
	ctx := context.Background()
	write(t, clone, "sub/a.sqlx", "stashed\n")
	write(t, clone, "sub/untracked.sql", "u\n")
	if _, err := r.StashPush(ctx, "wip"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(ctx); len(st.Changes) != 0 {
		t.Fatalf("after stash %+v", st)
	}
	list, err := r.Stashes(ctx)
	if err != nil || len(list) != 1 || list[0].Index != 0 || !strings.Contains(list[0].Subject, "wip") {
		t.Fatalf("stashes %+v %v", list, err)
	}
	files, err := r.StashFiles(ctx, 0)
	if err != nil || !slices.Equal(paths(files), []string{"M sub/a.sqlx", "U sub/untracked.sql"}) {
		t.Errorf("stash files %v %v", paths(files), err)
	}
	if b, _ := r.Show(ctx, StashRef(0)+"^3", "sub/untracked.sql", 100); b.Text != "u\n" {
		t.Errorf("untracked in the stash %+v", b)
	}
	if _, err := r.StashApply(ctx, 0, false); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.Stashes(ctx); len(list) != 1 {
		t.Error("apply dropped the stash")
	}
	_ = r.Restore(ctx, "sub/a.sqlx")
	_ = os.Remove(filepath.Join(clone, "sub/untracked.sql"))
	if _, err := r.StashApply(ctx, 0, true); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.Stashes(ctx); len(list) != 0 {
		t.Error("pop kept the stash")
	}
	_, _ = r.StashPush(ctx, "")
	if _, err := r.StashDrop(ctx, 0); err != nil {
		t.Fatal(err)
	}
}

func TestMergeInProgress(t *testing.T) {
	clone, _, r := fixture(t)
	ctx := context.Background()
	gitIn(t, clone, "switch", "-q", "-c", "other")
	write(t, clone, "sub/a.sqlx", "theirs\n")
	gitIn(t, clone, "commit", "-q", "-am", "other")
	gitIn(t, clone, "switch", "-q", "main")
	write(t, clone, "sub/a.sqlx", "ours\n")
	gitIn(t, clone, "commit", "-q", "-am", "ours")
	cmd := exec.Command("git", "merge", "other")
	cmd.Dir = clone
	_ = cmd.Run() // conflicts
	st, _ := r.Status(ctx)
	if st.Op != "merge" || len(st.Conflicts) != 1 || st.Conflicts[0].XY != "UU" {
		t.Fatalf("merge %+v", st)
	}
	if m := r.MergeMessage(ctx); !strings.HasPrefix(m, "Merge branch 'other'") {
		t.Errorf("merge message %q", m)
	}
	if _, err := r.Abort(ctx, "merge"); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(ctx); st.Op != "" {
		t.Errorf("after abort %+v", st)
	}
	cmd = exec.Command("git", "merge", "other")
	cmd.Dir = clone
	_ = cmd.Run()
	write(t, clone, "sub/a.sqlx", "both\n")
	_ = r.Stage(ctx, "sub/a.sqlx")
	if _, err := r.Continue(ctx, "merge", ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Status(ctx); st.Op != "" || len(st.Conflicts) != 0 {
		t.Errorf("after continue %+v", st)
	}
}

func TestWatch(t *testing.T) {
	clone, _, r := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Watch(ctx, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	write(t, clone, "sub/a.sqlx", "watched\n")
	if err := r.Stage(ctx, "sub/a.sqlx"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("staging did not wake Watch")
	}
	// Drain any trailing event from the stage operation.
	time.Sleep(100 * time.Millisecond)
	for len(events) > 0 {
		<-events
	}
	// Reading status and stashes must not re-trigger Watch (for example via
	// kqueue KQ_NOTE_ATTRIB / fsnotify.Chmod on .git/index on macOS).
	if _, err := r.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Stashes(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
		t.Fatal("read-only Status/Stashes re-triggered Watch")
	case <-time.After(250 * time.Millisecond):
	}
}

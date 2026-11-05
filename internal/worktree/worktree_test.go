package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
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

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture is a clone of an origin with main and colleague, whose project
// is in proj. The clone has a local branch feature/x, and in proj the
// untracked files a person's checkout has: node_modules, credentials and
// xf's config. profiles.yml is tracked.
func fixture(t *testing.T) (clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	base := t.TempDir()
	origin := filepath.Join(base, "origin")
	write(t, filepath.Join(origin, "proj", "a.sqlx"), "a")
	write(t, filepath.Join(origin, "proj", "profiles.yml"), "tracked")
	// xf's own .xf isn't ignored, as in a project that never thought of it.
	write(t, filepath.Join(origin, ".gitignore"), "node_modules/\n.df-credentials.json\n")
	gitIn(t, origin, "init", "-q", "-b", "main")
	gitIn(t, origin, "add", ".")
	gitIn(t, origin, "commit", "-q", "-m", "a")
	gitIn(t, origin, "branch", "colleague")
	clone = filepath.Join(base, "clone")
	gitIn(t, base, "clone", "-q", origin, clone)
	gitIn(t, clone, "branch", "feature/x")
	write(t, filepath.Join(clone, "proj", "node_modules", "@dataform", "core", "package.json"), "{}")
	if runtime.GOOS != "windows" {
		bin := filepath.Join(clone, "proj", "node_modules", ".bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "@dataform", "core", "package.json"), filepath.Join(bin, "core")); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(clone, "proj", ".df-credentials.json"), "{}")
	write(t, filepath.Join(clone, "proj", ".xf", "config.json"), `{"webTheme":"x"}`)
	return clone
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"feature/x":      "feature-x",
		"main":           "main",
		"a b//c":         "a-b-c",
		"/.weird./":      "weird",
		"":               "worktree",
		"v1.2_rc-3":      "v1.2_rc-3",
		"ash/JIRA-12#ok": "ash-JIRA-12-ok",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFolder(t *testing.T) {
	for in, want := range map[string]string{
		"feature/x":  "xf_feature-x",
		"xf_review":  "xf_review",
		"xf_":        "xf_worktree",
		"xf_ two":    "xf_two",
		"my review!": "xf_my-review",
		"":           "xf_worktree",
	} {
		if got := Folder(in); got != want {
			t.Errorf("Folder(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseList(t *testing.T) {
	out := "worktree /r\nHEAD aaa\nbranch refs/heads/main\n\n" +
		"worktree /r/.xf/worktrees/x\nHEAD bbb\nbranch refs/heads/feature/x\n\n" +
		"worktree /gone\nHEAD ccc\ndetached\nprunable gitdir file points to non-existent location\n\n"
	l := parseList(out)
	if len(l) != 3 || !l[0].Main || l[0].Branch != "main" || l[1].Main || l[1].Branch != "feature/x" ||
		l[2].Branch != "" || !l[2].Prunable {
		t.Errorf("parseList = %+v", l)
	}
	if l := parseList("worktree /bare\nbare\n\nworktree /w\nHEAD a\nbranch refs/heads/b\n"); len(l) != 1 || l[0].Branch != "b" {
		t.Errorf("a bare repository's entry is kept: %+v", l)
	}
}

func TestAddLinkRemove(t *testing.T) {
	clone := fixture(t)
	ctx := context.Background()

	if _, ok := Of(filepath.Join(clone, "proj")); ok {
		t.Fatal("the main checkout is a worktree")
	}
	top, err := Add(ctx, filepath.Join(clone, "proj"), "feature/x", "", Local)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(clone, ".xf", "worktrees", "xf_feature-x"); !samePath(top, want) {
		t.Errorf("top = %s, want %s", top, want)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, ".xf", "worktrees", ".gitignore")); !strings.Contains(string(b), "*") {
		t.Errorf(".xf/worktrees doesn't ignore itself: %q", b)
	}
	i, ok := Of(filepath.Join(top, "proj"))
	if !ok {
		t.Fatal("Of: not a worktree")
	}
	if i.Name != "xf_feature-x" || i.Rel != "proj" || !i.Own() || !samePath(i.MainRoot(), filepath.Join(clone, "proj")) {
		t.Errorf("Of = %+v (own %v, main root %s)", i, i.Own(), i.MainRoot())
	}
	// The worktrees folder ignores itself: the clone shows only its .xf.
	if st := gitIn(t, clone, "status", "--porcelain"); st != "?? proj/.xf/" {
		t.Errorf("the main checkout isn't clean: %q", st)
	}

	linked, err := Link(ctx, i, "dataform")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"node_modules", ".df-credentials.json", filepath.Join(".xf", "config.json")}
	if !slices.Equal(linked, want) {
		t.Errorf("linked %v, want %v", linked, want)
	}
	// node_modules is a folder of its own, for Dataform finds modules only
	// in the project, with the main checkout's files hard-linked in.
	pkg := filepath.Join("node_modules", "@dataform", "core", "package.json")
	if fi, err := os.Lstat(filepath.Join(i.Root(), "node_modules")); err != nil || !fi.IsDir() {
		t.Errorf("node_modules isn't a folder: %v, %v", fi, err)
	}
	a, _ := os.Stat(filepath.Join(clone, "proj", pkg))
	b, err := os.Stat(filepath.Join(i.Root(), pkg))
	if err != nil || !os.SameFile(a, b) {
		t.Errorf("node_modules isn't the main checkout's: %v", err)
	}
	if runtime.GOOS != "windows" {
		if l, err := os.Readlink(filepath.Join(i.Root(), "node_modules", ".bin", "core")); err != nil || l != filepath.Join("..", "@dataform", "core", "package.json") {
			t.Errorf("the .bin link is %q, %v", l, err)
		}
	}
	if _, err := os.Stat(filepath.Join(i.Root(), "node_modules.xf-linking")); err == nil {
		t.Error("the folder node_modules was made in is still there")
	}
	if again, err := Link(ctx, i, "dataform"); err != nil || len(again) != 0 {
		t.Errorf("a second Link linked %v, %v", again, err)
	}
	// dbt's profiles.yml is tracked, so the worktree has its own.
	if linked, _ := Link(ctx, i, "dbt"); len(linked) != 0 {
		t.Errorf("dbt linked %v", linked)
	}
	if fi, err := os.Lstat(filepath.Join(i.Root(), "profiles.yml")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("profiles.yml: %v, %v", fi, err)
	}

	// An untracked file of its own keeps it: git refuses. The linked
	// .xf shows as it does in the clone.
	write(t, filepath.Join(i.Root(), "mine.sqlx"), "x")
	// The linked node_modules is ignored in the worktree too, as in the
	// clone, though .gitignore says node_modules/.
	if b, _ := os.ReadFile(filepath.Join(clone, ".git", "info", "exclude")); !strings.Contains(string(b), "\n/proj/node_modules\n") {
		t.Errorf("info/exclude = %q", b)
	}
	if files, n, _ := Dirty(ctx, i, 5); n != 2 || !slices.Contains(files, "proj/mine.sqlx") {
		t.Errorf("Dirty = %v, %d", files, n)
	}
	if err := Remove(ctx, i, false); err == nil || !strings.Contains(err.Error(), "untracked") {
		t.Errorf("Remove of a dirty worktree: %v", err)
	}
	if err := os.Remove(filepath.Join(i.Root(), "mine.sqlx")); err != nil {
		t.Fatal(err)
	}

	// The hard-linked node_modules stays for git to remove.
	gone := Unlink(i, "dataform")
	if !slices.Equal(gone, want[1:]) {
		t.Errorf("unlinked %v, want %v", gone, want[1:])
	}
	if _, err := os.Stat(filepath.Join(i.Root(), ".xf")); err == nil {
		t.Error("the .xf folder Link made is still there")
	}
	if _, err := os.Stat(filepath.Join(clone, "proj", "node_modules")); err != nil {
		t.Errorf("Unlink took the main checkout's node_modules: %v", err)
	}
	if _, n, _ := Dirty(ctx, i, 5); n != 0 {
		t.Errorf("unlinked, still dirty: %d", n)
	}
	if err := Remove(ctx, i, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(top); err == nil {
		t.Error("the worktree is still there")
	}
	if b, err := os.ReadFile(filepath.Join(clone, "proj", pkg)); err != nil || string(b) != "{}" {
		t.Errorf("removing took the main checkout's node_modules: %q, %v", b, err)
	}
	if b := gitIn(t, clone, "branch", "--list", "feature/x"); b == "" {
		t.Error("Remove deleted the branch")
	}
}

// A node_modules an older xf linked, which Dataform can't load from,
// becomes a hard-linked folder on the next Link.
func TestLinkReplacesNodeModulesLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the older link was a junction there")
	}
	clone := fixture(t)
	ctx := context.Background()
	top, err := Add(ctx, filepath.Join(clone, "proj"), "feature/x", "", Local)
	if err != nil {
		t.Fatal(err)
	}
	i, _ := Of(filepath.Join(top, "proj"))
	if err := os.Symlink(filepath.Join(i.MainRoot(), "node_modules"), filepath.Join(i.Root(), "node_modules")); err != nil {
		t.Fatal(err)
	}
	linked, err := Link(ctx, i, "dataform")
	if err != nil || !slices.Contains(linked, "node_modules") {
		t.Fatalf("Link = %v, %v", linked, err)
	}
	if fi, err := os.Lstat(filepath.Join(i.Root(), "node_modules")); err != nil || !fi.IsDir() {
		t.Errorf("node_modules is still a link: %v, %v", fi, err)
	}
	// One the person installed is left alone.
	if err := os.RemoveAll(filepath.Join(i.Root(), "node_modules")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(i.Root(), "node_modules", "mine", "package.json"), "{}")
	if linked, _ := Link(ctx, i, "dataform"); slices.Contains(linked, "node_modules") {
		t.Error("Link replaced an installed node_modules")
	}
}

// A worktree's config is a copy of the main checkout's, its own: a
// setting saved in one doesn't reach the other. A link an older xf made
// to the main checkout's becomes one too.
func TestOwnConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the config is a plain copy there")
	}
	clone := fixture(t)
	ctx := context.Background()
	top, err := Add(ctx, filepath.Join(clone, "proj"), "feature/x", "", Local)
	if err != nil {
		t.Fatal(err)
	}
	i, _ := Of(filepath.Join(top, "proj"))
	mainConfig, wtConfig := filepath.Join(clone, "proj", Config), filepath.Join(i.Root(), Config)
	// An older xf's link to the main checkout's.
	if err := os.MkdirAll(filepath.Dir(wtConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	// An older xf linked it as Link does, to the main checkout git names.
	if err := os.Symlink(filepath.Join(i.MainRoot(), Config), wtConfig); err != nil {
		t.Fatal(err)
	}
	if linked, err := Link(ctx, i, "dataform"); err != nil || !slices.Contains(linked, Config) {
		t.Fatalf("Link = %v, %v", linked, err)
	}
	if l, err := os.Readlink(wtConfig); err != nil || l != i.OwnConfig() {
		t.Fatalf("the config links to %q, %v; want %s", l, err, i.OwnConfig())
	}
	if !strings.HasPrefix(i.OwnConfig(), i.GitDir) {
		t.Errorf("the own config %s isn't in %s", i.OwnConfig(), i.GitDir)
	}
	// A write through the link is the worktree's alone.
	write(t, wtConfig, `{"tablePrefix":"wt"}`)
	if b, _ := os.ReadFile(mainConfig); string(b) != `{"webTheme":"x"}` {
		t.Errorf("the main checkout's config is %q", b)
	}
	write(t, mainConfig, `{"tablePrefix":"main"}`)
	if b, _ := os.ReadFile(wtConfig); string(b) != `{"tablePrefix":"wt"}` {
		t.Errorf("the worktree's config is %q", b)
	}
	// It survives a removal git refuses: Unlink, then Link again.
	if gone := Unlink(i, "dataform"); !slices.Contains(gone, Config) {
		t.Errorf("Unlink took %v", gone)
	}
	if _, n, _ := Dirty(ctx, i, 5); n != 0 {
		t.Errorf("unlinked, still dirty: %d", n)
	}
	if _, err := Link(ctx, i, "dataform"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(wtConfig); string(b) != `{"tablePrefix":"wt"}` {
		t.Errorf("linked again, the worktree's config is %q", b)
	}
	// Removing the worktree takes it.
	Unlink(i, "dataform")
	if err := Remove(ctx, i, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(i.OwnConfig()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the own config outlived the worktree: %v", err)
	}
}

func TestAddRemoteAndTaken(t *testing.T) {
	clone := fixture(t)
	ctx := context.Background()
	top, err := Add(ctx, clone, "colleague", "", Remote)
	if err != nil {
		t.Fatal(err)
	}
	if up := gitIn(t, clone, "rev-parse", "--abbrev-ref", "colleague@{upstream}"); up != "origin/colleague" {
		t.Errorf("colleague tracks %s", up)
	}
	// Made from inside a worktree, the next goes in the main checkout's.
	top2, err := Add(ctx, top, "feature/x", "", Local)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(top2) != filepath.Dir(top) {
		t.Errorf("%s is not beside %s", top2, top)
	}
	// main is checked out in the clone: git refuses it.
	if _, err := Add(ctx, clone, "main", "", Local); err == nil {
		t.Error("Add made a second checkout of main")
	}
	// A folder that's taken gets -2.
	if err := os.MkdirAll(filepath.Join(clone, ".xf", "worktrees", "xf_b"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "branch", "b")
	top3, err := Add(ctx, clone, "b", "", Local)
	if err != nil || filepath.Base(top3) != "xf_b-2" {
		t.Errorf("Add b = %s, %v", top3, err)
	}
	// A new branch is made from the HEAD of the checkout asked from.
	top4, err := Add(ctx, top, "ash/new", "", New)
	if err != nil || filepath.Base(top4) != "xf_ash-new" {
		t.Fatalf("Add new = %s, %v", top4, err)
	}
	if got, want := gitIn(t, clone, "rev-parse", "ash/new"), gitIn(t, top, "rev-parse", "HEAD"); got != want {
		t.Errorf("ash/new is at %s, want colleague's HEAD %s", got, want)
	}
	// A name the person gave is the folder, with xf_ once.
	gitIn(t, clone, "branch", "c")
	if top5, err := Add(ctx, clone, "c", "xf_my review", Local); err != nil || filepath.Base(top5) != "xf_my-review" {
		t.Errorf("Add c named = %s, %v", top5, err)
	}
	if _, err := Add(ctx, clone, "ash/new", "", New); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("a second new ash/new: %v", err)
	}
	l, err := List(ctx, clone)
	if err != nil || len(l) != 6 || !l[0].Main {
		t.Fatalf("List = %+v, %v", l, err)
	}
}

func TestOfOthers(t *testing.T) {
	clone := fixture(t)
	ctx := context.Background()
	// A worktree made elsewhere, as an agent session's is.
	other := filepath.Join(filepath.Dir(clone), "elsewhere")
	gitIn(t, clone, "worktree", "add", "-q", other, "feature/x")
	i, ok := Of(filepath.Join(other, "proj"))
	if !ok || i.Own() || i.Name != "elsewhere" {
		t.Errorf("Of = %+v, %v (own %v)", i, ok, i.Own())
	}
	if p, err := Prefix(ctx, filepath.Join(other, "proj")); err != nil || p != "proj" {
		t.Errorf("Prefix = %q, %v", p, err)
	}
	if p, err := Prefix(ctx, other); err != nil || p != "." {
		t.Errorf("Prefix at the top = %q, %v", p, err)
	}
	// It can be removed too, but not while git locks it, forced or not.
	gitIn(t, clone, "worktree", "lock", other)
	for _, force := range []bool{false, true} {
		if err := Remove(ctx, i, force); err == nil || !strings.Contains(err.Error(), "locked") {
			t.Errorf("Remove of a locked worktree (force %v): %v", force, err)
		}
	}
	gitIn(t, clone, "worktree", "unlock", other)
	// Forced, its changes go with it.
	if err := os.WriteFile(filepath.Join(other, "proj", "mine.sqlx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, i, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Error("the worktree is still there")
	}
	// A submodule's .git file isn't a worktree's.
	sub := t.TempDir()
	write(t, filepath.Join(sub, ".git"), "gitdir: ../.git/modules/sub\n")
	if _, ok := Of(sub); ok {
		t.Error("a submodule is a worktree")
	}
}

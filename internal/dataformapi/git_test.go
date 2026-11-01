package dataformapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRepoNameFromURL(t *testing.T) {
	for _, tt := range []struct{ url, want string }{
		{"git@github.com:ashish10alex/shop-dataform.git", "shop-dataform"},
		{"https://github.com/ashish10alex/shop-dataform.git", "shop-dataform"},
		{"https://github.com/ashish10alex/shop-dataform", "shop-dataform"},
		{"https://github.com/ashish10alex/shop-dataform/", "shop-dataform"},
		{"ssh://git@gitlab.com:2222/group/sub/repo.git", "repo"},
		{"git@host:repo.git", "repo"},
		{"", ""},
		{"https://example.com/", "example.com"},
	} {
		if got := RepoNameFromURL(tt.url); got != tt.want {
			t.Errorf("RepoNameFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestParsers(t *testing.T) {
	if a, b := parseAheadBehind("2\t5"); a != 2 || b != 5 {
		t.Errorf("parseAheadBehind = %d, %d", a, b)
	}
	if a, b := parseAheadBehind(""); a != 0 || b != 0 {
		t.Errorf("parseAheadBehind(\"\") = %d, %d", a, b)
	}
	got := parsePorcelain(" M definitions/a.sqlx\n?? definitions/new.sqlx\nR  old.sqlx -> definitions/b.sqlx\n?? \"with space.sqlx\"")
	want := []string{"definitions/a.sqlx", "definitions/new.sqlx", "definitions/b.sqlx", "with space.sqlx"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parsePorcelain = %q, want %q", got, want)
	}
}

func TestWarnings(t *testing.T) {
	for _, tt := range []struct {
		name string
		g    GitState
		want []string
	}{
		{"in sync", GitState{Branch: "main", RemoteBranch: true}, nil},
		{"no remote branch", GitState{Branch: "feat"}, nil},
		{"ahead, behind and dirty", GitState{Branch: "feat", RemoteBranch: true, Ahead: 1, Behind: 3,
			Dirty: []string{"a.sqlx", "b.sqlx", "c.sqlx", "d.sqlx", "e.sqlx"}}, []string{
			"1 local commit not pushed to origin/feat won't run",
			"origin/feat has 3 commits you haven't pulled; they will run",
			"5 uncommitted files won't run: a.sqlx, b.sqlx, c.sqlx and 2 more",
		}},
	} {
		if got := tt.g.Warnings(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: Warnings = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestReadGit runs real git: a clone with an unpushed commit and a dirty
// file, in a project directory below the repository root.
func TestReadGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	origin := filepath.Join(dir, "shop.git")
	run(dir, "init", "--bare", "-b", "main", origin)
	clone := filepath.Join(dir, "clone")
	run(dir, "clone", origin, clone)
	project := filepath.Join(clone, "dataform")
	write(filepath.Join(project, "definitions", "a.sqlx"), "select 1")
	write(filepath.Join(clone, "README.md"), "outside the project")
	run(clone, "add", ".")
	run(clone, "commit", "-m", "one")
	run(clone, "push", "-u", "origin", "main")
	write(filepath.Join(project, "definitions", "b.sqlx"), "select 2")
	run(clone, "add", ".")
	run(clone, "commit", "-m", "two")
	write(filepath.Join(project, "definitions", "a.sqlx"), "select 3")
	write(filepath.Join(clone, "README.md"), "changed, but not in the project")

	g, err := ReadGit(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Unpushed) != 1 || !strings.HasSuffix(g.Unpushed[0], " two") {
		t.Errorf("Unpushed = %q, want the commit two", g.Unpushed)
	}
	g.Unpushed = nil
	want := GitState{Branch: "main", Origin: origin, RemoteBranch: true, Ahead: 1, Dirty: []string{"dataform/definitions/a.sqlx"}}
	if !reflect.DeepEqual(g, want) {
		t.Errorf("ReadGit = %+v, want %+v", g, want)
	}

	if b := g.Blocker(); b != "" {
		t.Errorf("pushed branch: Blocker = %q", b)
	}
	run(clone, "checkout", "-q", "-b", "feat")
	if g, _ := ReadGit(context.Background(), project); !strings.Contains(g.Blocker(), "origin/feat doesn't exist") {
		t.Errorf("unpushed branch: Blocker = %q", g.Blocker())
	}
	run(clone, "checkout", "-q", "--detach")
	if g, _ := ReadGit(context.Background(), project); g.Branch != "" {
		t.Errorf("detached HEAD: Branch = %q", g.Branch)
	}
	if _, err := ReadGit(context.Background(), dir); err == nil {
		t.Error("no error outside a git repository")
	}
}

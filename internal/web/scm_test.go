package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
)

// scmRepo is a repository with one commit whose project is its df
// folder; git has a fixed identity and none of the user's config.
func scmRepo(t *testing.T) (repo, root string, git func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t",
		"GIT_COMMITTER_EMAIL": "t@t", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1"} {
		t.Setenv(k, v)
	}
	repo = t.TempDir()
	git = func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	root = filepath.Join(repo, "df")
	writeTree(t, root, map[string]string{"definitions/orders.sqlx": "SELECT 1\n", "includes/x.js": "x\n"})
	writeTree(t, repo, map[string]string{"outside.txt": "o\n"})
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	return repo, root, git
}

func TestSCMStatusDiffStageCommit(t *testing.T) {
	repo, root, git := scmRepo(t)
	writeTree(t, root, map[string]string{"definitions/orders.sqlx": "SELECT 2\n", "includes/new.js": "n\n"})
	writeTree(t, repo, map[string]string{"outside.txt": "o2\n"})
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})

	var v scmView
	getJSON(t, c, ts.URL+"/api/scm", &v)
	if v.Prefix != "df/" || v.Branch != "main" || len(v.Staged) != 0 || len(v.Changes) != 3 {
		t.Fatalf("status %+v", v)
	}
	files := map[string]scmFile{}
	for _, f := range v.Changes {
		files[f.Path] = f
	}
	if f := files["df/definitions/orders.sqlx"]; f.Kind != "M" || f.File != "definitions/orders.sqlx" {
		t.Errorf("orders %+v", f)
	}
	if f := files["df/includes/new.js"]; f.Kind != "U" {
		t.Errorf("new %+v", f)
	}
	if f := files["outside.txt"]; f.File != "" {
		t.Errorf("outside the project %+v", f)
	}

	var d diffView
	getJSON(t, c, ts.URL+"/api/scm/diff?side=changes&path=df/definitions/orders.sqlx", &d)
	if d.Old.Text != "SELECT 1\n" || d.New.Text != "SELECT 2\n" || !d.Live || d.File != "definitions/orders.sqlx" {
		t.Errorf("diff %+v", d)
	}
	d = diffView{}
	getJSON(t, c, ts.URL+"/api/scm/diff?side=changes&path=outside.txt", &d)
	if d.Live || d.New.Text != "o2\n" {
		t.Errorf("outside diff %+v", d)
	}

	var ok okView
	postJSON(t, c, ts.URL+"/api/scm/commit", map[string]any{"message": "nothing"}, &ok)
	if ok.OK || !strings.Contains(ok.Message, "nothing staged") {
		t.Errorf("commit with nothing staged: %+v", ok)
	}
	postJSON(t, c, ts.URL+"/api/scm/stage", map[string]any{"paths": []string{"df/definitions/orders.sqlx"}}, &ok)
	if !ok.OK {
		t.Fatalf("stage %+v", ok)
	}
	d = diffView{}
	getJSON(t, c, ts.URL+"/api/scm/diff?side=staged&path=df/definitions/orders.sqlx", &d)
	if d.Old.Text != "SELECT 1\n" || d.New.Text != "SELECT 2\n" || d.Live {
		t.Errorf("staged diff %+v", d)
	}
	ok = okView{}
	postJSON(t, c, ts.URL+"/api/scm/commit", map[string]any{"message": "second\n\nwhy"}, &ok)
	if !ok.OK || !strings.Contains(ok.Message, "second") {
		t.Fatalf("commit %+v", ok)
	}
	if got := git("log", "-1", "--format=%s"); got != "second" {
		t.Errorf("HEAD is %q", got)
	}
	// gsh: the index's version is set, from the version it was.
	was := "x\n"
	postJSON(t, c, ts.URL+"/api/scm/index", map[string]any{"path": "df/includes/x.js", "text": "x\ny\n", "was": was}, &ok)
	if !ok.OK || git("show", ":df/includes/x.js") != "x\ny" {
		t.Errorf("set index %+v", ok)
	}
	ok = okView{}
	postJSON(t, c, ts.URL+"/api/scm/index", map[string]any{"path": "df/includes/x.js", "text": "z\n", "was": was}, &ok)
	if ok.OK {
		t.Error("a stale gsh was taken")
	}

	var lv logView
	getJSON(t, c, ts.URL+"/api/scm/log", &lv)
	if len(lv.Commits) != 2 || lv.More || lv.Commits[0].Subject != "second" {
		t.Errorf("log %+v", lv)
	}
	var out struct {
		Entries []struct {
			Args []string `json:"args"`
			OK   bool     `json:"ok"`
		} `json:"entries"`
	}
	getJSON(t, c, ts.URL+"/api/scm/output", &out)
	if len(out.Entries) < 3 || out.Entries[0].Args[1] != "add" {
		t.Errorf("output %+v", out)
	}
}

// Discard puts an untracked file in the trash, and a copy of a tracked
// one before git puts back the index's version.
func TestSCMDiscard(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the trash is looked at in the macOS one")
	}
	trashHome(t)
	_, root, _ := scmRepo(t)
	writeTree(t, root, map[string]string{"definitions/orders.sqlx": "SELECT mine\n", "includes/new.js": "n\n"})
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})
	var ok okView
	postJSON(t, c, ts.URL+"/api/scm/discard", map[string]any{"paths": []string{"df/definitions/orders.sqlx", "df/includes/new.js"}}, &ok)
	if !ok.OK {
		t.Fatalf("discard %+v", ok)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "definitions/orders.sqlx")); string(b) != "SELECT 1\n" {
		t.Errorf("orders is %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "includes/new.js")); err == nil {
		t.Error("new.js is still there")
	}
	trash := filepath.Join(os.Getenv("HOME"), ".Trash")
	if b, _ := os.ReadFile(filepath.Join(trash, "orders.sqlx")); string(b) != "SELECT mine\n" {
		t.Errorf("the trash's orders is %q", b)
	}
	if _, err := os.Stat(filepath.Join(trash, "new.js")); err != nil {
		t.Error("new.js isn't in the trash")
	}
	ok = okView{}
	postJSON(t, c, ts.URL+"/api/scm/discard", map[string]any{"paths": []string{"df/includes/x.js"}}, &ok)
	if ok.OK {
		t.Error("discarded a file with no change")
	}
}

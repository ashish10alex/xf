package web

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// writeTree writes files, by path relative to root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func rowsByPath(v filesView) map[string]fileRow {
	m := map[string]fileRow{}
	for _, f := range v.Files {
		m[f.Path] = f
	}
	return m
}

// The tree lists git's files, tracked and not, without ignored ones,
// marked with their status and actions.
func TestFilesFromGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	// The project is a folder of the repository.
	root := filepath.Join(repo, "df")
	writeTree(t, root, map[string]string{
		"definitions/orders.sqlx":    "SELECT 1\n",
		"definitions/customers.sqlx": "SELECT 1\n",
		"definitions/sources.js":     "declare({})\n",
		"includes/x.js":              "x\n",
		".gitignore":                 "node_modules/\n*.log\n",
	})
	writeTree(t, repo, map[string]string{"outside.txt": "x\n"})
	git("add", ".")
	git("commit", "-q", "-m", "init")
	writeTree(t, root, map[string]string{
		"definitions/customers.sqlx": "SELECT 2\n",
		"includes/new.js":            "y\n",
		"node_modules/a/index.js":    "z\n",
		"debug.log":                  "w\n",
	})
	if err := os.Remove(filepath.Join(root, "includes", "x.js")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"definitions/empty", "docs/empty"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTree(t, root, map[string]string{"docs/notes.md": "x\n"})
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})
	var v filesView
	getJSON(t, c, ts.URL+"/api/files", &v)
	if !v.Git {
		t.Fatal("not listed by git")
	}
	var paths []string
	for _, f := range v.Files {
		paths = append(paths, f.Path)
	}
	want := []string{".gitignore", "definitions/customers.sqlx", "definitions/orders.sqlx", "definitions/sources.js", "docs/notes.md", "includes/new.js"}
	if !slices.Equal(paths, want) {
		t.Errorf("files = %v, want %v", paths, want)
	}
	rows := rowsByPath(v)
	if r := rows["definitions/customers.sqlx"]; r.Git != "M" || r.Action != "p.mart.customers" || r.Actions != 1 {
		t.Errorf("customers = %+v", r)
	}
	if r := rows["includes/new.js"]; r.Git != "U" || r.Action != "" {
		t.Errorf("new.js = %+v", r)
	}
	// The table comes before its assertion.
	if r := rows["definitions/orders.sqlx"]; r.Git != "" || r.Action != "p.mart.orders" || r.Actions != 2 {
		t.Errorf("orders = %+v", r)
	}
	if slices.Sort(v.Dirs); !slices.Equal(v.Dirs, []string{"definitions/empty", "docs/empty"}) {
		t.Errorf("dirs = %v, want [definitions/empty docs/empty]", v.Dirs)
	}
}

// A file's first action is the one the tree lists for it, and a file with
// none answers "" rather than an error.
func TestFileAction(t *testing.T) {
	_, ts, c := newTest(t, frontend.Deps{Root: t.TempDir(), Engine: newFakeEngine(testGraph())})
	for file, want := range map[string]project.ActionID{
		"definitions/orders.sqlx":    "p.mart.orders",
		"definitions/customers.sqlx": "p.mart.customers",
		"README.md":                  "",
	} {
		var v struct {
			Action project.ActionID `json:"action"`
		}
		getJSON(t, c, ts.URL+"/api/files/action?file="+url.QueryEscape(file), &v)
		if v.Action != want {
			t.Errorf("%s: action = %q, want %q", file, v.Action, want)
		}
	}
}

// Outside a repository the tree walks the project, without build output,
// packages and dot folders.
func TestFilesWalked(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"definitions/orders.sqlx": "SELECT 1\n",
		"README.md":               "x\n",
		"node_modules/a/i.js":     "x\n",
		"target/run.sql":          "x\n",
		".vscode/settings.json":   "{}\n",
	})
	if err := os.MkdirAll(filepath.Join(root, "includes"), 0o755); err != nil {
		t.Fatal(err)
	}
	files, dirs, err := walkFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, []string{"README.md", "definitions/orders.sqlx"}) || !slices.Equal(dirs, []string{"includes"}) {
		t.Errorf("files = %v, dirs = %v", files, dirs)
	}
}

func TestFilesNewRenameDelete(t *testing.T) {
	trashHome(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"definitions/orders.sqlx": "SELECT 1\n", "includes/x.js": "x\n"})
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})
	exists := func(rel string) bool {
		_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	}

	postJSON(t, c, ts.URL+"/api/files/new", map[string]any{"path": "definitions/marts/new.sqlx"}, nil)
	postJSON(t, c, ts.URL+"/api/files/new", map[string]any{"path": "definitions/staging", "dir": true}, nil)
	if !exists("definitions/marts/new.sqlx") || !exists("definitions/staging") {
		t.Fatal("new file or folder not made")
	}
	if st := postStatus(t, c, ts.URL+"/api/files/new", map[string]any{"path": "includes/x.js"}, nil); st == http.StatusOK {
		t.Error("made a file over one that exists")
	}
	if got := readFile(t, filepath.Join(root, "includes", "x.js")); got != "x\n" {
		t.Errorf("x.js = %q", got)
	}

	postJSON(t, c, ts.URL+"/api/files/rename", map[string]any{"from": "includes/x.js", "to": "includes/lib/y.js"}, nil)
	if exists("includes/x.js") || !exists("includes/lib/y.js") {
		t.Fatal("not renamed")
	}
	for _, bad := range []map[string]any{
		{"from": "includes", "to": "includes/lib/inner"},               // into itself
		{"from": "includes/lib/y.js", "to": "definitions/orders.sqlx"}, // over another file
		{"from": "includes/gone.js", "to": "includes/z.js"},
		{"from": "includes/lib/y.js", "to": "../y.js"},
		{"from": "includes/lib/y.js", "to": ".git/y.js"},
	} {
		if st := postStatus(t, c, ts.URL+"/api/files/rename", bad, nil); st == http.StatusOK {
			t.Errorf("rename %v worked", bad)
		}
	}
	if !exists("includes/lib/y.js") || readFile(t, filepath.Join(root, "definitions", "orders.sqlx")) != "SELECT 1\n" {
		t.Fatal("a refused rename moved something")
	}

	postJSON(t, c, ts.URL+"/api/files/delete", map[string]any{"path": "includes"}, nil)
	if exists("includes") {
		t.Fatal("not deleted")
	}
	for _, bad := range []string{"", "..", "/etc", ".git", "definitions/../../x"} {
		if st := postStatus(t, c, ts.URL+"/api/files/delete", map[string]any{"path": bad}, nil); st == http.StatusOK {
			t.Errorf("deleted %q", bad)
		}
	}
	if !exists("definitions/orders.sqlx") {
		t.Fatal("deleted too much")
	}
}

// trashHome sends this test's trash to a temporary folder.
func trashHome(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "windows":
		t.Skip("the Recycle Bin is the system's")
	case "darwin":
		t.Setenv("HOME", t.TempDir())
	default:
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
}

// A rename says which actions it renames and how many refer to them.
func TestFilesRenameCheck(t *testing.T) {
	orders := project.Target{Database: "p", Schema: "mart", Name: "orders"}
	named := project.Target{Database: "p", Schema: "mart", Name: "named"}
	g := project.NewGraph([]*project.Action{
		{ID: orders.ID(), Target: orders, Kind: project.KindTable, FileName: "definitions/orders.sqlx"},
		{ID: "p.mart.orders_assertions_rowConditions", Target: project.Target{Database: "p", Schema: "mart", Name: "orders_assertions_rowConditions"},
			Kind: project.KindAssertion, FileName: "definitions/orders.sqlx", ParentAction: &orders, DependencyTargets: []project.Target{orders}},
		{ID: "p.mart.report", Target: project.Target{Database: "p", Schema: "mart", Name: "report"}, Kind: project.KindView,
			FileName: "definitions/report.sqlx", DependencyTargets: []project.Target{orders}},
		{ID: "p.mart.daily", Target: project.Target{Database: "p", Schema: "mart", Name: "daily"}, Kind: project.KindView,
			FileName: "definitions/daily.sqlx", DependencyTargets: []project.Target{orders}},
		{ID: named.ID(), Target: named, Kind: project.KindTable, FileName: "definitions/other.sqlx"},
	})
	root := t.TempDir()
	writeTree(t, root, map[string]string{"definitions/orders.sqlx": "x", "definitions/other.sqlx": "x", "definitions/report.sqlx": "x"})
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(g)})
	check := func(from, to string) renameCheck {
		t.Helper()
		var rc renameCheck
		postJSON(t, c, ts.URL+"/api/files/rename", map[string]any{"from": from, "to": to, "check": true}, &rc)
		return rc
	}
	rc := check("definitions/orders.sqlx", "definitions/orders_v2.sqlx")
	if len(rc.Renames) != 1 || rc.Renames[0].ID != orders.ID() || rc.Renames[0].Name != "orders_v2" || rc.Refs != 2 || rc.Files != 2 {
		t.Errorf("orders → orders_v2: %+v", rc)
	}
	if rc := check("definitions/orders.sqlx", "definitions/marts/orders.sqlx"); len(rc.Renames) != 0 {
		t.Errorf("a move renames: %+v", rc)
	}
	// other.sqlx names its action in its config.
	if rc := check("definitions/other.sqlx", "definitions/renamed.sqlx"); len(rc.Renames) != 0 {
		t.Errorf("a config-named action renamed: %+v", rc)
	}
	if !fileExists(filepath.Join(root, "definitions", "orders.sqlx")) {
		t.Error("a check renamed the file")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

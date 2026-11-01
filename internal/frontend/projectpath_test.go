package frontend

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/session"
)

func TestIsPathQuery(t *testing.T) {
	for q, want := range map[string]bool{
		"~": true, "~/repos": true, "/Users": true,
		"": false, "dbt": false, "./dbt": false, "../x": false, "~user": false, "repos/dbt": false,
	} {
		if got := IsPathQuery(q); got != want {
			t.Errorf("IsPathQuery(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestExpandPath(t *testing.T) {
	home := filepath.FromSlash("/home/me")
	for q, want := range map[string]string{
		"~":              "/home/me",
		"~/":             "/home/me",
		"~/repos/dbt/":   "/home/me/repos/dbt",
		"/x/./y/../z//":  "/x/z",
		"/home/me/repos": "/home/me/repos",
	} {
		if got := ExpandPath(home, q); got != filepath.FromSlash(want) {
			t.Errorf("ExpandPath(%q) = %q, want %q", q, got, want)
		}
	}
}

// pathTree makes home with repos/ holding a Dataform project, a dbt
// project, a folder that is both, a plain folder, a dot-folder and a file.
func pathTree(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("repos/warehouse/workflow_settings.yaml")
	write("repos/Wdbt/dbt_project.yml")
	write("repos/both/workflow_settings.yaml")
	write("repos/both/dbt_project.yml")
	write("repos/wasm/main.go")
	write("repos/.wcache/x")
	write("repos/wfile")
	return home
}

func rowsText(rows []PathRow) string {
	var lines []string
	for _, r := range rows {
		s := r.Path
		if r.Backend != "" {
			s += " " + r.Backend
		}
		if r.Note != "" {
			s += " (" + r.Note + ")"
		}
		lines = append(lines, s)
	}
	return strings.Join(lines, "\n")
}

func TestPathRows(t *testing.T) {
	home := pathTree(t)
	remembered := []session.Project{
		{Root: filepath.Join(home, "repos", "warehouse"), Backend: "dataform", LastVisited: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)},
		{Root: filepath.Join(home, "repos", "Wdbt"), Backend: "dbt"}, // found by xf search
	}
	for _, c := range []struct{ q, want string }{
		// The folder typed, then what its last part completes to:
		// projects first, any case, no dot-folders, no files.
		{"~/repos/w", `~/repos/w (no such folder)
~/repos/warehouse dataform (opened before)
~/repos/Wdbt dbt (listed)
~/repos/wasm`},
		{"~/repos/.w", `~/repos/.w (no such folder)
~/repos/.wcache`},
		{"~/repos/both", `~/repos/both dataform
~/repos/both dbt`},
		{"~/repos/wasm", `~/repos/wasm (not a project)`},
		{"~/repos/wfile", `~/repos/wfile (not a folder)`},
		{"~/repos/warehouse/", `~/repos/warehouse/ dataform (opened before)`},
	} {
		if got := rowsText(PathRows(home, c.q, remembered)); got != c.want {
			t.Errorf("PathRows(%q):\n%s\nwant:\n%s", c.q, got, c.want)
		}
	}
	rows := PathRows(home, "~/repos/wasm", nil)
	if !strings.Contains(rows[0].Why, "dbt_project.yml") || strings.Contains(rows[0].Why, "run xf") {
		t.Errorf("Why = %q, want the markers looked for and no command-line hint", rows[0].Why)
	}
	if rows[0].Root != filepath.Join(home, "repos", "wasm") {
		t.Errorf("Root = %q", rows[0].Root)
	}
}

func TestPathRowsCapsFolders(t *testing.T) {
	home := t.TempDir()
	for i := range maxPathRows + 7 {
		if err := os.Mkdir(filepath.Join(home, fmt.Sprintf("d%03d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := PathRows(home, "~/", nil)
	if len(rows) != 1+maxPathRows+1 {
		t.Fatalf("got %d rows, want the folder, %d folders and a more row", len(rows), maxPathRows)
	}
	if last := rows[len(rows)-1]; !last.More || last.Path != "7 more · keep typing" {
		t.Errorf("last row = %+v", last)
	}
}

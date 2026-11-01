package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// tree makes the files under a new folder, each a slash path, and returns
// the folder.
func tree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// found lists what Search found under root as "rel tool", sorted.
func found(t *testing.T, root string, got []Found) []string {
	t.Helper()
	var out []string
	for _, f := range got {
		rel, err := filepath.Rel(root, f.Root)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel)+" "+f.Backend)
	}
	slices.Sort(out)
	return out
}

func TestSearch(t *testing.T) {
	root := tree(t,
		"a/workflow_settings.yaml",
		"a/nested/dbt_project.yml", // inside a project: skipped
		"b/legacy/dataform.json",
		"c/dbt_project.yml",
		"c/dbt_packages/pkg/dbt_project.yml",
		"both/workflow_settings.yaml",
		"both/dbt_project.yml",
		".hidden/dbt_project.yml",
		"x/node_modules/p/dataform.json",
		"x/target/dbt_project.yml",
		"x/notes.md",
	)
	for _, tt := range []struct {
		want string
		got  []string
	}{
		{"", []string{"a dataform", "b/legacy dataform", "both dataform", "c dbt"}},
		{DBT, []string{"a/nested dbt", "both dbt", "c dbt"}},
		{Dataform, []string{"a dataform", "b/legacy dataform", "both dataform"}},
	} {
		got, err := Search(root, tt.want)
		if err != nil {
			t.Fatal(err)
		}
		if g := found(t, root, got); !slices.Equal(g, tt.got) {
			t.Errorf("Search(%q) = %v, want %v", tt.want, g, tt.got)
		}
	}
}

func TestSearchRootIsAProject(t *testing.T) {
	root := tree(t, "dbt_project.yml", "sub/workflow_settings.yaml")
	got, err := Search(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if g := found(t, root, got); !slices.Equal(g, []string{". dbt"}) {
		t.Errorf("Search = %v, want the root alone", g)
	}
}

func TestSearchFollowsASymlinkedRootOnly(t *testing.T) {
	real := tree(t, "p/dbt_project.yml")
	elsewhere := tree(t, "q/dbt_project.yml")
	links := t.TempDir()
	link := filepath.Join(links, "code")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(real, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	got, err := Search(link, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Root != filepath.Join(link, "p") {
		t.Errorf("Search = %+v, want %s alone, spelled through the link", got, filepath.Join(link, "p"))
	}
}

func TestSearchSkipsUnreadableFolders(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("permissions don't keep this user out")
	}
	root := tree(t, "locked/p/dbt_project.yml", "open/dbt_project.yml")
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	got, err := Search(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if g := found(t, root, got); !slices.Equal(g, []string{"open dbt"}) {
		t.Errorf("Search = %v, want open alone", g)
	}
}

func TestSearchErrors(t *testing.T) {
	root := tree(t, "file.txt")
	for _, tt := range []struct {
		dir, want, errHas string
	}{
		{filepath.Join(root, "missing"), "", "missing"},
		{filepath.Join(root, "file.txt"), "", "is not a folder"},
		{root, "sqlmesh", `unknown backend "sqlmesh"`},
	} {
		if _, err := Search(tt.dir, tt.want); err == nil || !strings.Contains(err.Error(), tt.errHas) {
			t.Errorf("Search(%s, %q) error = %v, want one with %q", tt.dir, tt.want, err, tt.errHas)
		}
	}
}

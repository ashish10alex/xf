package web

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSourceFindFile(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.js"), []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	root, ts, c := newSourceTest(t, map[string]string{
		"definitions/orders.sqlx":       "SELECT 1\n",
		"definitions/staging/near.sqlx": "SELECT 1\n",
		"definitions/near.sqlx":         "SELECT 1\n",
		"includes/constants.js":         "module.exports = {}\n",
		"README.md":                     "# x\n",
		".git/config":                   "[core]\n",
	})
	type found struct{ File, Action, Folder string }
	find := func(from, name string) found {
		t.Helper()
		var v found
		postJSON(t, c, ts.URL+"/api/source/findfile", map[string]string{"file": from, "name": name}, &v)
		return v
	}
	for _, tc := range []struct {
		from, name string
		want       found
	}{
		// Beside the file first, then from the root.
		{"definitions/staging/near.sqlx", "near.sqlx", found{File: "definitions/staging/near.sqlx"}},
		{"definitions/orders.sqlx", "near.sqlx", found{File: "definitions/near.sqlx"}},
		{"definitions/orders.sqlx", "README.md", found{File: "README.md"}},
		{"definitions/orders.sqlx", "../includes/constants.js", found{File: "includes/constants.js"}},
		// A require's name, its .js added.
		{"definitions/orders.sqlx", "includes/constants", found{File: "includes/constants.js"}},
		// An action's file comes with its action.
		{"README.md", "definitions/orders.sqlx", found{File: "definitions/orders.sqlx", Action: "p.mart.orders"}},
		{"README.md", filepath.ToSlash(filepath.Join(root, "includes", "constants.js")), found{File: "includes/constants.js"}},
		{"README.md", "includes", found{Folder: "includes"}},
		{"README.md", "nothing.sqlx", found{}},
		{"README.md", ".git/config", found{}},
		{"README.md", "../" + filepath.Base(outside) + "/secret.js", found{}},
		{"README.md", filepath.ToSlash(filepath.Join(outside, "secret.js")), found{}},
		{"README.md", "", found{}},
	} {
		if got := find(tc.from, tc.name); got != tc.want {
			t.Errorf("gf %q from %s = %+v, want %+v", tc.name, tc.from, got, tc.want)
		}
	}
}

func TestSourcePaths(t *testing.T) {
	_, ts, c := newSourceTest(t, map[string]string{
		"definitions/orders.sqlx":       "SELECT 1\n",
		"definitions/staging/near.sqlx": "SELECT 1\n",
		"includes/constants.js":         "module.exports = {}\n",
		".git/config":                   "[core]\n",
		".sqlfluff":                     "x\n",
	})
	list := func(from, dir string) []string {
		t.Helper()
		var v struct{ Entries []dirEntry }
		postJSON(t, c, ts.URL+"/api/source/paths", map[string]string{"file": from, "dir": dir}, &v)
		var out []string
		for _, e := range v.Entries {
			n := e.Name
			if e.Dir {
				n += "/"
			}
			out = append(out, n)
		}
		slices.Sort(out)
		return out
	}
	for _, tc := range []struct {
		from, dir string
		want      []string
	}{
		{"definitions/orders.sqlx", "", []string{".sqlfluff", "definitions/", "includes/"}},
		{"definitions/orders.sqlx", "definitions/", []string{"orders.sqlx", "staging/"}},
		{"definitions/orders.sqlx", "./", []string{"orders.sqlx", "staging/"}},
		{"definitions/staging/near.sqlx", "../../includes/", []string{"constants.js"}},
		{"definitions/orders.sqlx", "../../", nil},
		{"definitions/orders.sqlx", ".git/", nil},
		{"definitions/orders.sqlx", "nothing/", nil},
	} {
		if got := list(tc.from, tc.dir); !slices.Equal(got, tc.want) {
			t.Errorf("paths %q from %s = %v, want %v", tc.dir, tc.from, got, tc.want)
		}
	}
}

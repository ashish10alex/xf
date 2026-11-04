package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/session"
)

// The picker's folder search finds the projects in a folder typed from ~,
// as they are found, marks those the picker lists already, and adds the
// ones the page keeps.
func TestFindAndAddProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	write := func(rel, file string) string {
		dir := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte("x: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	shop := write("repos/shop", "workflow_settings.yaml")
	jaffle := write("repos/team/jaffle", "dbt_project.yml")
	write("repos/node_modules/pkg", "dbt_project.yml") // skipped
	var added []string
	s, err := New(Config{Token: "secret",
		Projects: func() []session.Project { return []session.Project{{Root: shop, Backend: "dataform"}} },
		Add: func(root, tool string) (bool, error) {
			added = append(added, root+" "+tool)
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar

	var b bootView
	getJSON(t, c, ts.URL+"/api/boot", &b)
	if !b.Find {
		t.Error("boot doesn't say the picker can find projects")
	}

	find := func(path string) (*http.Response, []findLine) {
		t.Helper()
		res, err := c.Post(ts.URL+"/api/picker/find", "application/json", strings.NewReader(`{"path":`+quote(path)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		var lines []findLine
		sc := bufio.NewScanner(res.Body)
		for res.StatusCode == http.StatusOK && sc.Scan() {
			var l findLine
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, l)
		}
		return res, lines
	}
	res, lines := find("~/repos")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("find answered %d", res.StatusCode)
	}
	if len(lines) != 3 || !lines[2].Done {
		t.Fatalf("lines = %+v, want two projects then done", lines)
	}
	got := map[string]foundProject{}
	for _, l := range lines[:2] {
		got[l.Found.Name] = *l.Found
	}
	if p := got["shop"]; p.Backend != "dataform" || !p.Known || p.Path != "~"+string(filepath.Separator)+filepath.Join("repos", "shop") {
		t.Errorf("shop = %+v, want dataform, known, under ~", p)
	}
	if p := got["jaffle"]; p.Backend != "dbt" || p.Known || p.Root != jaffle || p.Rel != "team/jaffle" {
		t.Errorf("jaffle = %+v, want dbt, not known", p)
	}

	for _, bad := range []string{"repos", "~/nope"} {
		if res, _ := find(bad); res.StatusCode != http.StatusBadRequest {
			t.Errorf("find %q answered %d, want 400", bad, res.StatusCode)
		}
	}

	var a struct{ Added int }
	postJSON(t, c, ts.URL+"/api/picker/add", map[string]any{"projects": []map[string]string{{"root": jaffle, "backend": "dbt"}}}, &a)
	if a.Added != 1 || len(added) != 1 || added[0] != jaffle+" dbt" {
		t.Errorf("added %d: %v", a.Added, added)
	}
	// A folder that isn't that tool's project is refused.
	res, err = c.Post(ts.URL+"/api/picker/add", "application/json", strings.NewReader(`{"projects":[{"root":`+quote(jaffle)+`,"backend":"dataform"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("adding jaffle as dataform answered %d", res.StatusCode)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

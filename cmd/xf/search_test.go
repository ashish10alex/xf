package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/session"
)

// mkProject makes dir a project, with the tool's marker file.
func mkProject(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, marker), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearchProjectsExpandsHomeAndListsOnce(t *testing.T) {
	home := t.TempDir()
	mkProject(t, filepath.Join(home, "code", "shop"), "workflow_settings.yaml")
	got, err := searchProjects(home, []string{"~", "~/code"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Root != filepath.Join(home, "code", "shop") || got[0].Backend != backend.Dataform {
		t.Errorf("searchProjects = %+v, want ~/code/shop once", got)
	}
}

func TestAddFound(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	shop, lake := filepath.Join(home, "code", "shop"), filepath.Join(home, "code", "lake")
	if _, err := session.Add(dir, lake, backend.DBT); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := addFound(&out, dir, home, []backend.Found{{Root: shop, Backend: backend.Dataform}, {Root: lake, Backend: backend.DBT}}); err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.Separator)
	want := "shop  dataform  ~" + sep + "code" + sep + "shop  added\n" +
		"lake  dbt       ~" + sep + "code" + sep + "lake  already known\n" +
		"\n2 projects found: 1 added, 1 already known\n"
	if out.String() != want {
		t.Errorf("addFound printed\n%s\nwant\n%s", out.String(), want)
	}

	out.Reset()
	if err := addFound(&out, dir, home, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no Dataform or dbt projects found\n" {
		t.Errorf("addFound of none printed %q", out.String())
	}
}

func TestRunSearchTakesFlagsAfterDirs(t *testing.T) {
	state, code := t.TempDir(), t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	mkProject(t, filepath.Join(code, "both"), "workflow_settings.yaml")
	mkProject(t, filepath.Join(code, "both"), "dbt_project.yml")
	if err := runSearch([]string{code, "--backend", "dbt"}); err != nil {
		t.Fatal(err)
	}
	s := session.Load(session.Path(filepath.Join(state, "xf", "sessions"), filepath.Join(code, "both")))
	if s.Backend != backend.DBT || !s.LastVisited.IsZero() {
		t.Errorf("session = %+v, want dbt's, not visited", s)
	}
	if err := runSearch([]string{"--backend", "sqlmesh", code}); err == nil || !strings.Contains(err.Error(), "sqlmesh") {
		t.Errorf("runSearch with an unknown backend: %v", err)
	}
}

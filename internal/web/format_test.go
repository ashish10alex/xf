package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/frontend"
)

// fakeSQLFluff writes a sqlfluff that upper-cases its input, as a shell
// script, so the endpoint can be tried without the real one.
func fakeSQLFluff(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	bin := filepath.Join(t.TempDir(), "sqlfluff")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntr a-z A-Z\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestFormatEndpoint(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "definitions"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"definitions/orders.sqlx", "definitions/notes.md"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, ts, c := newTestProject(t, &Project{Deps: frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())}, SqlfluffBin: fakeSQLFluff(t)})

	var got formatView
	text := "config {\n  type: \"table\"\n}\nselect a from ${ref(\"x\")}\n"
	if code := postStatus(t, c, ts.URL+"/api/format", map[string]any{"file": "definitions/orders.sqlx", "text": text}, &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if want := "config {\n  type: \"table\"\n}\nSELECT A FROM ${ref(\"x\")}\n"; got.Text != want || !got.Changed {
		t.Errorf("got %+v, want text %q", got, want)
	}

	var sc formatView
	if code := postStatus(t, c, ts.URL+"/api/format", map[string]any{"scratch": true, "text": "select 1"}, &sc); code != 200 || sc.Text != "SELECT 1" {
		t.Errorf("scratch: %d %+v", code, sc)
	}

	var bad errorView
	if code := postStatus(t, c, ts.URL+"/api/format", map[string]any{"file": "definitions/notes.md", "text": "x"}, &bad); code != 422 || !strings.Contains(bad.Error, ".sqlx and .sql") {
		t.Errorf("a markdown file: %d %+v", code, bad)
	}
	if code := postStatus(t, c, ts.URL+"/api/format", map[string]any{"file": "../outside.sqlx", "text": "x"}, nil); code != 403 {
		t.Errorf("a file outside the project: %d", code)
	}
}

func TestFormatEndpointWithoutSQLFluff(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	_, ts, c := newTestProject(t, &Project{Deps: frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())}})
	var bad errorView
	if code := postStatus(t, c, ts.URL+"/api/format", map[string]any{"scratch": true, "text": "select 1"}, &bad); code != 422 || !strings.HasPrefix(bad.Error, "sqlfluff not found") {
		t.Errorf("%d %+v", code, bad)
	}

	// A sqlx file's config block formats without sqlfluff, and a note
	// says the SQL didn't.
	if err := os.MkdirAll(filepath.Join(root, "definitions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "definitions/orders.sqlx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got formatView
	text := "config {type: \"view\"}\nselect 1\n"
	if code := postStatus(t, c, ts.URL+"/api/format", map[string]any{"file": "definitions/orders.sqlx", "text": text}, &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if want := "config {\n  type: \"view\"\n}\nselect 1\n"; got.Text != want || !got.Changed || !strings.Contains(got.Note, "not the SQL: sqlfluff not found") {
		t.Errorf("got %+v, want text %q and a note", got, want)
	}
}

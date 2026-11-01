package compilecache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/testutil"
)

var today = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// newProject makes a small Dataform project and a CLI installed the way npm
// does it: a link into the @dataform/cli package.
func newProject(t *testing.T) (root, bin string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "project")
	for _, f := range []struct{ path, body string }{
		{"workflow_settings.yaml", "defaultProject: p\n"},
		{"definitions/staging/a.sqlx", "select 1"},
		{"includes/helpers.js", "module.exports = {};"},
		{"README.md", "# readme"},
		{"cli/node_modules/@dataform/cli/package.json", `{"version": "3.0.26"}`},
		{"cli/node_modules/@dataform/cli/bin/dataform.js", ""},
	} {
		write(t, filepath.Join(root, f.path), f.body)
	}
	bin = filepath.Join(dir, "dataform")
	testutil.Symlink(t, filepath.Join(root, "cli/node_modules/@dataform/cli/bin/dataform.js"), bin)
	return root, bin
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

// touch rewrites a file with a later modification time, as a save does.
func touch(t *testing.T, path, body string) {
	t.Helper()
	write(t, path, body)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func take(t *testing.T, root, bin string, at time.Time) Fingerprint {
	t.Helper()
	fp, err := Take(root, bin, at)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestOutdatedSaysWhatChanged(t *testing.T) {
	root, bin := newProject(t)
	before := take(t, root, bin, today)
	if r := Outdated(before, take(t, root, bin, today)); r != "" {
		t.Errorf("nothing changed, but outdated: %q", r)
	}

	for _, c := range []struct {
		name, want string
		change     func()
	}{
		{"a README", "", func() { touch(t, filepath.Join(root, "README.md"), "# new") }},
		{"a dotfile", "", func() {
			write(t, filepath.Join(root, "definitions", ".DS_Store"), "finder")
			write(t, filepath.Join(root, "definitions", "staging", ".a.sqlx.swp"), "vim")
		}},
		{"a definition", "project files changed", func() { touch(t, filepath.Join(root, "definitions/staging/a.sqlx"), "select 2") }},
		{"a new include", "project files changed", func() { write(t, filepath.Join(root, "includes/constants.js"), "") }},
		{"the settings", "project files changed", func() { touch(t, filepath.Join(root, "workflow_settings.yaml"), "defaultProject: q\n") }},
		{"a package.json", "project files changed", func() { write(t, filepath.Join(root, "package.json"), "{}") }},
		{"the CLI", "the dataform CLI changed", func() {
			touch(t, filepath.Join(root, "cli/node_modules/@dataform/cli/package.json"), `{"version": "3.0.27"}`)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := take(t, root, bin, today)
			c.change()
			if got := Outdated(before, take(t, root, bin, today)); got != c.want {
				t.Errorf("Outdated = %q, want %q", got, c.want)
			}
		})
	}

	if got := Outdated(take(t, root, bin, today.Add(-24*time.Hour)), take(t, root, bin, today)); got != "compiled on an earlier day" {
		t.Errorf("a day later: %q", got)
	}
}

func TestStoreKeepsOneCompilePerProject(t *testing.T) {
	store := &Store{Dir: t.TempDir()}
	a, b := t.TempDir(), t.TempDir()
	if e, err := store.Load(a); e != nil || err != nil {
		t.Fatalf("nothing saved yet: %v, %v", e, err)
	}
	fp := Fingerprint{Files: "f", CLI: "c", Day: "2026-10-01"}
	at := today.Round(0)
	for _, json := range []string{`{"first": true}`, "{\"targets\": []}\n"} {
		if err := store.Save(a, Entry{Fingerprint: fp, CompiledAt: at, Took: 2 * time.Second, JSON: []byte(json)}); err != nil {
			t.Fatal(err)
		}
	}
	e, err := store.Load(a)
	if err != nil || e == nil {
		t.Fatalf("Load: %v, %v", e, err)
	}
	if e.Fingerprint != fp || !e.CompiledAt.Equal(at) || e.Took != 2*time.Second || string(e.JSON) != "{\"targets\": []}\n" {
		t.Errorf("loaded %+v, JSON %q", e, e.JSON)
	}
	if e, err := store.Load(b); e != nil || err != nil {
		t.Errorf("another project's compile was loaded: %v, %v", e, err)
	}
	entries, _ := os.ReadDir(store.Dir)
	for _, d := range entries {
		if strings.HasPrefix(d.Name(), "tmp-") {
			t.Errorf("left a temporary file: %s", d.Name())
		}
	}
}

// Through npm's Windows shim, upgrading @dataform/cli still changes the
// stamp: it's the package.json beside the bundle the shim runs.
func TestCLIStampThroughTheNpmShim(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "dataform.cmd")
	pkg := filepath.Join(dir, "node_modules", "@dataform", "cli", "package.json")
	write(t, shim, "@ECHO off")
	write(t, filepath.Join(filepath.Dir(pkg), "bundle.js"), "")
	write(t, pkg, `{"version": "3.0.48"}`)
	before := cliStamp(shim)
	write(t, pkg, `{"version": "3.0.49", "upgraded": true}`)
	if cliStamp(shim) == before {
		t.Error("upgrading the CLI behind the shim kept its stamp")
	}
}

func TestTakeDbt(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "dbt")
	write(t, bin, "#!/bin/sh")
	write(t, filepath.Join(root, "dbt_project.yml"), "name: p\n")
	write(t, filepath.Join(root, "models", "a.sql"), "select 1")
	write(t, filepath.Join(root, "target", "compiled", "a.sql"), "select 1")
	t.Setenv("DBT_PROFILES_DIR", t.TempDir())
	takeDbt := func() Fingerprint {
		t.Helper()
		fp, err := TakeDbt(root, bin, today)
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	for _, c := range []struct {
		name, want string
		change     func()
	}{
		{"dbt's own output", "", func() { touch(t, filepath.Join(root, "target", "compiled", "a.sql"), "select 2") }},
		{"a README", "", func() { write(t, filepath.Join(root, "README"), "hello") }},
		{"a model", "project files changed", func() { touch(t, filepath.Join(root, "models", "a.sql"), "select 2") }},
		{"a new macro", "project files changed", func() { write(t, filepath.Join(root, "macros", "m.sql"), "{% macro m() %}{% endmacro %}") }},
		{"the project file", "project files changed", func() { touch(t, filepath.Join(root, "dbt_project.yml"), "name: q\n") }},
		{"an installed package", "project files changed", func() { write(t, filepath.Join(root, "dbt_packages", "u", "macros", "x.sql"), "") }},
		{"the profile outside the project", "project files changed", func() {
			write(t, filepath.Join(os.Getenv("DBT_PROFILES_DIR"), "profiles.yml"), "p: {}\n")
		}},
		{"the CLI", "the dbt CLI changed", func() { touch(t, bin, "#!/bin/sh\n# upgraded") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := takeDbt()
			c.change()
			if got := Outdated(before, takeDbt()); got != c.want {
				t.Errorf("Outdated = %q, want %q", got, c.want)
			}
		})
	}
}

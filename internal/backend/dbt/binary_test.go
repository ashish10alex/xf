package dbt

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// venvDbt puts a dbt in root's virtualenv dir and returns its path.
func venvDbt(t *testing.T, root, dir string) string {
	t.Helper()
	rel := filepath.Join("bin", "dbt")
	if runtime.GOOS == "windows" {
		rel = filepath.Join("Scripts", "dbt.exe")
	}
	p := filepath.Join(root, dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The project's own dbt-core, in its .venv or venv, comes before the dbt on
// PATH (dbt v2, say), whichever shell started xf; a flag or $DBT_BIN still
// wins.
func TestResolveBinaryFindsTheProjectVenv(t *testing.T) {
	onPath := func(name string) (string, error) {
		if name == "dbt" || filepath.IsAbs(name) {
			return name, nil
		}
		return "", exec.ErrNotFound
	}
	noEnv := func(string) string { return "" }
	root := t.TempDir()
	if got, _ := ResolveBinaryFrom("", "", root, noEnv, onPath); got != "dbt" {
		t.Errorf("no venv: %q, want dbt from PATH", got)
	}
	plain := venvDbt(t, root, "venv")
	if got, _ := ResolveBinaryFrom("", "", root, noEnv, onPath); got != plain {
		t.Errorf("venv: %q, want %q", got, plain)
	}
	dot := venvDbt(t, root, ".venv")
	if got, _ := ResolveBinaryFrom("", "", root, noEnv, onPath); got != dot {
		t.Errorf(".venv and venv: %q, want .venv's %q", got, dot)
	}
	flag := filepath.Join(t.TempDir(), "dbt")
	if got, _ := ResolveBinaryFrom(flag, "--dbt-bin", root, noEnv, onPath); got != flag {
		t.Errorf("flag: %q, want %q", got, flag)
	}
	env := func(k string) string {
		if k == BinEnv {
			return flag
		}
		return ""
	}
	if got, _ := ResolveBinaryFrom("", "", root, env, onPath); got != flag {
		t.Errorf("$DBT_BIN: %q, want %q", got, flag)
	}
}

func TestResolveBinaryHints(t *testing.T) {
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	noEnv := func(string) string { return "" }
	for _, bin := range []string{`C:\tools\dbt.exe`, "/opt/dbt"} {
		_, err := ResolveBinaryFrom(bin, "--dbt-bin", t.TempDir(), noEnv, missing)
		if err == nil || !strings.Contains(err.Error(), "could not find a dbt binary at") {
			t.Errorf("%s: %v", bin, err)
		}
	}
}


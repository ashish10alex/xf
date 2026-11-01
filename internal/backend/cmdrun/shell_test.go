package cmdrun

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestShellJoin(t *testing.T) {
	args := []string{"dbt", "build", "--select", "+shop.marts.orders+", "my file.sql", "", "a$b", "it's", `C:\x\dbt.exe`}
	for goos, want := range map[string]string{
		"darwin":  `dbt build --select +shop.marts.orders+ 'my file.sql' '' 'a$b' 'it'\''s' 'C:\x\dbt.exe'`,
		"linux":   `dbt build --select +shop.marts.orders+ 'my file.sql' '' 'a$b' 'it'\''s' 'C:\x\dbt.exe'`,
		"windows": `dbt build --select +shop.marts.orders+ "my file.sql" "" 'a$b' "it's" C:\x\dbt.exe`,
	} {
		if got := shellJoin(goos, args); got != want {
			t.Errorf("%s:\n got %s\nwant %s", goos, got, want)
		}
	}
}

// The command names the binary it runs: by name when that's what PATH
// finds, else by its path, relative to the project when inside it (a
// project venv's dbt-core while dbt v2 is on PATH, say).
func TestCommandName(t *testing.T) {
	exe := func(dir, name string) string {
		t.Helper()
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pathDir, root := t.TempDir(), t.TempDir()
	onPath := exe(pathDir, "dbt")
	inVenv := exe(filepath.Join(root, ".venv", "bin"), "dbt")
	elsewhere := exe(t.TempDir(), "dbt")
	t.Setenv("PATH", pathDir)
	for bin, want := range map[string]string{
		onPath:    "dbt",
		inVenv:    filepath.Join(".venv", "bin", filepath.Base(inVenv)),
		elsewhere: elsewhere,
	} {
		if got := CommandName(bin, "dbt", root); got != want {
			t.Errorf("CommandName(%s) = %q, want %q", bin, got, want)
		}
	}
	if got := CommandName(exe(root, "dbt-local"), "dbt", root); got != "."+string(filepath.Separator)+filepath.Base(exe(root, "dbt-local")) {
		t.Errorf("a binary in the root itself: %q", got)
	}
}

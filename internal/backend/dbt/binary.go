package dbt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// venvDirs are the virtualenvs in a project root where dbt-core is looked
// for, in order.
var venvDirs = []string{".venv", "venv"}

// VenvBinary is the dbt in one of the project's virtualenvs (.venv, venv),
// or "" when there's none.
func VenvBinary(root string) string {
	if root == "" {
		return ""
	}
	rel := filepath.Join("bin", "dbt")
	if runtime.GOOS == "windows" {
		rel = filepath.Join("Scripts", "dbt.exe")
	}
	for _, dir := range venvDirs {
		p := filepath.Join(root, dir, rel)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// ResolveBinaryFrom finds the dbt binary: bin (from source, a flag or a
// config file) first, then $DBT_BIN, then the project's virtualenv (see
// VenvBinary), then "dbt" on PATH. The venv comes before PATH so that
// whichever client starts the engine, from whatever shell, the project's
// own dbt-core compiles it. getenv and lookPath are injected so tests don't
// depend on the real environment.
func ResolveBinaryFrom(bin, source, root string, getenv func(string) string, lookPath func(string) (string, error)) (string, error) {
	candidate := "dbt"
	switch {
	case bin != "":
		candidate = bin
	case getenv(BinEnv) != "":
		candidate, source = getenv(BinEnv), "$"+BinEnv
	case VenvBinary(root) != "":
		return VenvBinary(root), nil
	default:
		source = "PATH"
	}
	path, err := lookPath(candidate)
	if err != nil {
		var hint string
		if strings.ContainsAny(candidate, `/\`) {
			hint = fmt.Sprintf("could not find a dbt binary at %q (from %s)", candidate, source)
		} else {
			hint = fmt.Sprintf("could not find %q on PATH", candidate)
			if source != "PATH" {
				hint += " (from " + source + ")"
			}
		}
		return "", fmt.Errorf("%s. Install dbt (dbt v2: brew install dbt-labs/dbt/dbt, or dbt Labs' installer on Windows; dbt-core: pip install dbt-core dbt-bigquery, in the project's .venv to have xf find it), or set --dbt-bin, $%s or \"dbtBin\" in .xf/config.json", hint, BinEnv)
	}
	return path, nil
}

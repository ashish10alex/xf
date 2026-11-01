package pyrefly

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// goos is the OS whose layout of a virtual environment is looked for;
// tests change it.
var goos = runtime.GOOS

// Interpreter is a Python found for a project: its path, and where it was
// found (".venv", "venv", "env", "$VIRTUAL_ENV", "$CONDA_PREFIX", "PATH").
type Interpreter struct {
	Path  string `json:"path"`
	Where string `json:"where"`
}

// rootVenvs are the folders at a project's root that are looked in for a
// virtual environment, in order; DefaultInterpreter takes the first two.
var rootVenvs = []string{".venv", "venv", "env"}

// venvPython is the interpreter of the environment in dir: bin/python, or
// Scripts\python.exe on Windows.
func venvPython(dir string) string {
	if goos == "windows" {
		return filepath.Join(dir, "Scripts", "python.exe")
	}
	return filepath.Join(dir, "bin", "python")
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// Interpreters are the Pythons xf offers for the project at root: a
// virtual environment at its root (.venv, venv, env), $VIRTUAL_ENV's,
// $CONDA_PREFIX's, then python3 and python on PATH. Each path is listed
// once.
func Interpreters(root string, getenv func(string) string, lookPath func(string) (string, error)) []Interpreter {
	var out []Interpreter
	seen := map[string]bool{}
	add := func(path, where string) {
		if path == "" || seen[path] || !isFile(path) {
			return
		}
		seen[path] = true
		out = append(out, Interpreter{Path: path, Where: where})
	}
	for _, d := range rootVenvs {
		add(venvPython(filepath.Join(root, d)), d)
	}
	if v := getenv("VIRTUAL_ENV"); v != "" {
		add(venvPython(v), "$VIRTUAL_ENV")
	}
	if v := getenv("CONDA_PREFIX"); v != "" {
		if goos == "windows" {
			add(filepath.Join(v, "python.exe"), "$CONDA_PREFIX")
		} else {
			add(filepath.Join(v, "bin", "python"), "$CONDA_PREFIX")
		}
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := lookPath(name); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			add(p, "PATH")
		}
	}
	return out
}

// DefaultInterpreter is the Python used until one is picked: the root's
// .venv, else its venv; "" leaves it to pyrefly.
func DefaultInterpreter(root string) Interpreter {
	for _, d := range rootVenvs[:2] {
		if p := venvPython(filepath.Join(root, d)); isFile(p) {
			return Interpreter{Path: p, Where: d}
		}
	}
	return Interpreter{}
}

// Label is how xf names the interpreter at path: its virtual environment's
// folder (".venv", "dbt"), else its file ("python3"); "" for none.
func Label(path string) string {
	if path == "" {
		return ""
	}
	env := filepath.Dir(filepath.Dir(path))
	if isFile(filepath.Join(env, "pyvenv.cfg")) {
		return filepath.Base(env)
	}
	return filepath.Base(path)
}

// ErrNotFound is pyrefly missing from the interpreter's environment and
// PATH.
var ErrNotFound = errors.New("pyrefly not found: uv tool install pyrefly")

// FindBin is the pyrefly to run: beside interpreter (in its environment's
// bin), else on PATH.
func FindBin(interpreter string, lookPath func(string) (string, error)) (string, error) {
	name := "pyrefly"
	if goos == "windows" {
		name = "pyrefly.exe"
	}
	if interpreter != "" {
		if p := filepath.Join(filepath.Dir(interpreter), name); isFile(p) {
			return p, nil
		}
	}
	if p, err := lookPath("pyrefly"); err == nil {
		return p, nil
	}
	return "", ErrNotFound
}

// ConfigFile is the file pyrefly is configured with, for a file in dir:
// the nearest pyrefly.toml, or pyproject.toml with a [tool.pyrefly]
// table, in dir or a folder above it; "" for none.
func ConfigFile(dir string) string {
	for {
		if p := filepath.Join(dir, "pyrefly.toml"); isFile(p) {
			return p
		}
		if p := filepath.Join(dir, "pyproject.toml"); isFile(p) && hasPyreflyTable(p) {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

var pyreflyTable = regexp.MustCompile(`^\s*\[\s*tool\.pyrefly\s*[\].]`)

func hasPyreflyTable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if pyreflyTable.MatchString(sc.Text()) {
			return true
		}
	}
	return false
}

// ConfigName is the file WriteConfig writes, at the project root.
const ConfigName = "pyrefly.toml"

// ConfigText is the pyrefly.toml xf writes: the project's Python files,
// without the folders tools fill (dbt's target and packages, node's
// modules, virtual environments, logs). It names no interpreter: that is
// each user's, kept in xf's session.
const ConfigText = `# Written by xf. pyrefly's options: https://pyrefly.org/en/docs/configuration/
project-includes = ["**/*.py"]
project-excludes = [
  "**/target/**",
  "**/dbt_packages/**",
  "**/node_modules/**",
  "**/.venv/**",
  "**/venv/**",
  "**/logs/**",
]
`

// WriteConfig writes ConfigText to root's pyrefly.toml, which must not
// exist yet, and answers with its path.
func WriteConfig(root string) (string, error) {
	path := filepath.Join(root, ConfigName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return path, errors.New(ConfigName + " already exists")
		}
		return "", err
	}
	if _, err := f.WriteString(ConfigText); err != nil {
		_ = f.Close()
		return "", err
	}
	return path, f.Close()
}

// InLibrary says whether path is an installed package's file rather than
// the project's: in a site-packages or dist-packages folder, or in a
// virtual environment (a folder with a pyvenv.cfg) at or under root.
func InLibrary(root, path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "site-packages" || part == "dist-packages" {
			return true
		}
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	dir := filepath.Dir(path)
	for {
		if isFile(filepath.Join(dir, "pyvenv.cfg")) {
			return true
		}
		if dir == root || filepath.Dir(dir) == dir {
			return false
		}
		dir = filepath.Dir(dir)
	}
}

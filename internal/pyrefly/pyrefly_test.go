package pyrefly

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/pyrefly/pyreflytest"
)

func TestMain(m *testing.M) {
	pyreflytest.Main()
	os.Exit(m.Run())
}

// published collects what a Client's Diagnostics is told.
type published struct {
	mu   sync.Mutex
	cond *sync.Cond
	got  map[string][]Diagnostic
	ver  map[string]int
}

func newPublished() *published {
	p := &published{got: map[string][]Diagnostic{}, ver: map[string]int{}}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *published) record(path string, version int, ds []Diagnostic) {
	p.mu.Lock()
	p.got[path], p.ver[path] = ds, version
	p.mu.Unlock()
	p.cond.Broadcast()
}

// wait waits for path's errors to satisfy ok.
func (p *published) wait(t *testing.T, path string, ok func(version int, ds []Diagnostic) bool) []Diagnostic {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	go func() { time.Sleep(5 * time.Second); p.cond.Broadcast() }()
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if ds, have := p.got[path]; have && ok(p.ver[path], ds) {
			return ds
		}
		if time.Now().After(deadline) {
			t.Fatalf("no such errors for %s: last %v (version %d)", path, p.got[path], p.ver[path])
		}
		p.cond.Wait()
	}
}

func startFake(t *testing.T, root string, interp *string, mu *sync.Mutex, pub *published) *Client {
	t.Helper()
	t.Setenv(pyreflytest.Env, "1")
	c, err := Start(context.Background(), Options{
		Bin: os.Args[0], Root: root,
		Interpreter: func() string { mu.Lock(); defer mu.Unlock(); return *interp },
		Diagnostics: pub.record,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c
}

func TestClient(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.py")
	var mu sync.Mutex
	interp := "/py/one"
	pub := newPublished()
	c := startFake(t, root, &interp, &mu, pub)

	v, _, _, err := c.Sync(file, "ok\nx = BAD")
	if err != nil || v != 1 {
		t.Fatalf("Sync = %d, %v", v, err)
	}
	ds := pub.wait(t, file, func(v int, ds []Diagnostic) bool {
		return v == 1 && len(ds) == 1 && strings.Contains(ds[0].Message, "/py/one")
	})
	if d := ds[0]; d.Range.Start != (Position{1, 4}) || d.Severity != "error" || d.Code != "bad-line" || d.Message != "bad line (python: /py/one)" {
		t.Errorf("diagnostic = %+v", d)
	}
	// The same text again sends nothing and has its errors.
	if v, ds, have, _ := c.Sync(file, "ok\nx = BAD"); v != 1 || !have || len(ds) != 1 {
		t.Errorf("Sync again = %d, %v, %v", v, ds, have)
	}
	if v, _, have, _ := c.Sync(file, "fine\nBAD\nBAD"); v != 2 || have {
		t.Errorf("Sync changed = %d, %v", v, have)
	}
	pub.wait(t, file, func(v int, ds []Diagnostic) bool { return v == 2 && len(ds) == 2 })

	// Another interpreter: pyrefly asks again.
	mu.Lock()
	interp = "/py/two"
	mu.Unlock()
	c.ConfigChanged()
	pub.wait(t, file, func(v int, ds []Diagnostic) bool { return len(ds) == 2 && strings.Contains(ds[0].Message, "/py/two") })

	ctx := context.Background()
	h, err := c.Hover(ctx, file, Position{0, 2})
	if err != nil || h == nil || !strings.HasPrefix(h.Markdown, "```python\nfine\n```") || h.Range == nil || h.Range.End.Character != 4 {
		t.Errorf("Hover = %+v, %v", h, err)
	}
	locs, err := c.Definition(ctx, file, Position{0, 1})
	if err != nil || len(locs) != 1 || locs[0].Path != file || locs[0].Range.Start != (Position{0, 4}) {
		t.Errorf("Definition = %+v, %v", locs, err)
	}
	items, incomplete, err := c.Complete(ctx, file, Position{0, 0})
	if err != nil || incomplete || len(items) != 2 || items[0].Kind != "variable" || items[1].Kind != "function" || items[1].Detail != "() -> int" || items[1].Insert != "beta" {
		t.Errorf("Complete = %+v, %v, %v", items, incomplete, err)
	}
	sig, err := c.SignatureHelp(ctx, file, Position{0, 0})
	if err != nil || sig == nil || sig.Label != "f(a, b)" || !slices.Equal(sig.Params, [][2]int{{2, 3}, {5, 6}}) || sig.Active != 1 {
		t.Errorf("SignatureHelp = %+v, %v", sig, err)
	}

	c.CloseFile(file)
	if len(c.Open()) != 0 {
		t.Errorf("open after close: %v", c.Open())
	}
	c.Stop()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("pyrefly still running after Stop")
	}
	if _, _, _, err := c.Sync(file, "x"); !errors.Is(err, ErrStopped) {
		t.Errorf("Sync after Stop = %v", err)
	}
	if _, err := c.Hover(ctx, file, Position{}); !errors.Is(err, ErrStopped) {
		t.Errorf("Hover after Stop = %v", err)
	}
}

func TestStartFails(t *testing.T) {
	_, err := Start(context.Background(), Options{Bin: filepath.Join(t.TempDir(), "nope"), Root: t.TempDir()})
	if err == nil {
		t.Fatal("Start of a missing binary worked")
	}
}

func TestURI(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a b", "c#.py")
	if got := PathOf(URI(p)); got != p {
		t.Errorf("PathOf(URI(%q)) = %q", p, got)
	}
	if PathOf("untitled:1") != "" {
		t.Error("a URI that isn't a file has a path")
	}
}

func TestMarkdownOf(t *testing.T) {
	for raw, want := range map[string]string{
		`{"kind":"markdown","value":"**x**"}`:     "**x**",
		`"plain"`:                                 "plain",
		`{"language":"python","value":"x: int"}`:  "```python\nx: int\n```",
		`["a",{"language":"python","value":"b"}]`: "a\n\n```python\nb\n```",
		`null`: "",
	} {
		if got := markdownOf([]byte(raw)); got != want {
			t.Errorf("markdownOf(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestUTF16Index(t *testing.T) {
	if got := utf16Index("f(é, 😀x, x)", "x", 4); got != 7 {
		t.Errorf("utf16Index = %d, want 7", got)
	}
	if got := utf16Index("f(a)", "b", 0); got != -1 {
		t.Errorf("utf16Index of a missing name = %d", got)
	}
}

func touch(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInterpreters(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	touch(t, venvPython(filepath.Join(root, "venv")), "")
	touch(t, venvPython(filepath.Join(root, "env")), "")
	touch(t, venvPython(other), "")
	touch(t, filepath.Join(other, "bin", "python3"), "")
	env := map[string]string{"VIRTUAL_ENV": other, "CONDA_PREFIX": filepath.Join(root, "missing")}
	lookPath := func(name string) (string, error) {
		if name == "python3" {
			return filepath.Join(other, "bin", "python3"), nil
		}
		return "", exec.ErrNotFound
	}
	got := Interpreters(root, func(k string) string { return env[k] }, lookPath)
	want := []Interpreter{
		{venvPython(filepath.Join(root, "venv")), "venv"},
		{venvPython(filepath.Join(root, "env")), "env"},
		{venvPython(other), "$VIRTUAL_ENV"},
		{filepath.Join(other, "bin", "python3"), "PATH"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Interpreters =\n%v\nwant\n%v", got, want)
	}
	// venv/ is the default without a .venv/, which wins once there.
	if d := DefaultInterpreter(root); d.Where != "venv" {
		t.Errorf("DefaultInterpreter = %+v", d)
	}
	touch(t, venvPython(filepath.Join(root, ".venv")), "")
	if d := DefaultInterpreter(root); d.Where != ".venv" || d.Path != venvPython(filepath.Join(root, ".venv")) {
		t.Errorf("DefaultInterpreter = %+v", d)
	}
	if d := DefaultInterpreter(t.TempDir()); d.Path != "" {
		t.Errorf("DefaultInterpreter of a bare project = %+v", d)
	}
}

func TestLabel(t *testing.T) {
	env := filepath.Join(t.TempDir(), "dbt")
	touch(t, filepath.Join(env, "pyvenv.cfg"), "")
	touch(t, filepath.Join(env, "bin", "python"), "")
	if got := Label(filepath.Join(env, "bin", "python")); got != "dbt" {
		t.Errorf("Label of a venv's python = %q", got)
	}
	if got := Label("/opt/homebrew/bin/python3"); got != "python3" {
		t.Errorf("Label of a python = %q", got)
	}
}

func TestFindBin(t *testing.T) {
	venv := t.TempDir()
	py := venvPython(venv)
	touch(t, py, "")
	none := func(string) (string, error) { return "", exec.ErrNotFound }
	onPath := func(string) (string, error) { return "/usr/bin/pyrefly", nil }
	if _, err := FindBin(py, none); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindBin with none = %v", err)
	}
	if got, _ := FindBin(py, onPath); got != "/usr/bin/pyrefly" {
		t.Errorf("FindBin = %q, want PATH's", got)
	}
	bin := filepath.Join(filepath.Dir(py), "pyrefly")
	if goos == "windows" {
		bin += ".exe"
	}
	touch(t, bin, "")
	if got, _ := FindBin(py, onPath); got != bin {
		t.Errorf("FindBin = %q, want the venv's", got)
	}
}

func TestConfigFile(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "models", "py")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ConfigFile(sub); got != "" && strings.HasPrefix(got, root) {
		t.Errorf("ConfigFile = %q, want none", got)
	}
	touch(t, filepath.Join(root, "pyproject.toml"), "[project]\nname = \"x\"\n")
	if got := ConfigFile(sub); strings.HasPrefix(got, root) {
		t.Errorf("a pyproject.toml without [tool.pyrefly] counts: %q", got)
	}
	touch(t, filepath.Join(root, "pyproject.toml"), "[project]\nname = \"x\"\n\n[tool.pyrefly]\nproject-includes = []\n")
	if got := ConfigFile(sub); got != filepath.Join(root, "pyproject.toml") {
		t.Errorf("ConfigFile = %q, want the pyproject.toml", got)
	}
	path, err := WriteConfig(filepath.Join(root, "models"))
	if err != nil {
		t.Fatal(err)
	}
	if got := ConfigFile(sub); got != path {
		t.Errorf("ConfigFile = %q, want the nearer %q", got, path)
	}
	if b, _ := os.ReadFile(path); string(b) != ConfigText {
		t.Errorf("WriteConfig wrote %q", b)
	}
	if _, err := WriteConfig(filepath.Join(root, "models")); err == nil {
		t.Error("WriteConfig wrote over a pyrefly.toml")
	}
}

func TestInLibrary(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, ".venv", "pyvenv.cfg"), "")
	for path, want := range map[string]bool{
		filepath.Join(root, "models", "a.py"):                                      false,
		filepath.Join(root, ".venv", "lib", "python3.12", "site-packages", "x.py"): true,
		filepath.Join(root, ".venv", "bin", "activate_this.py"):                    true,
		"/usr/lib/python3/dist-packages/y.py":                                      true,
		"/opt/python/lib/python3.12/os.py":                                         false,
	} {
		if got := InLibrary(root, path); got != want {
			t.Errorf("InLibrary(%s) = %v", path, got)
		}
	}
}

// TestRealPyrefly runs the pyrefly on PATH, when there is one.
func TestRealPyrefly(t *testing.T) {
	bin, err := exec.LookPath("pyrefly")
	if err != nil || testing.Short() {
		t.Skip("no pyrefly on PATH")
	}
	root := t.TempDir()
	if _, err := WriteConfig(root); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "a.py")
	text := "def f(a: int, b: str) -> int:\n    return a\n\nx: str = f(1, \"b\")\n"
	touch(t, file, text)
	pub := newPublished()
	c, err := Start(context.Background(), Options{Bin: bin, Root: root, Diagnostics: pub.record})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	if _, _, _, err := c.Sync(file, text); err != nil {
		t.Fatal(err)
	}
	ds := pub.wait(t, file, func(_ int, ds []Diagnostic) bool { return len(ds) > 0 })
	if ds[0].Code != "bad-assignment" || ds[0].Range.Start.Line != 3 {
		t.Errorf("errors = %+v", ds)
	}
	ctx := context.Background()
	if h, err := c.Hover(ctx, file, Position{3, 9}); err != nil || h == nil || !strings.Contains(h.Markdown, "def f") {
		t.Errorf("Hover = %+v, %v", h, err)
	}
	if locs, err := c.Definition(ctx, file, Position{3, 9}); err != nil || len(locs) != 1 || locs[0].Range.Start.Line != 0 {
		t.Errorf("Definition = %+v, %v", locs, err)
	}
	if sig, err := c.SignatureHelp(ctx, file, Position{3, 14}); err != nil || sig == nil || sig.Active != 1 {
		t.Errorf("SignatureHelp = %+v, %v", sig, err)
	}
	items, _, err := c.Complete(ctx, file, Position{3, 9})
	if err != nil || !slices.ContainsFunc(items, func(c Completion) bool { return c.Label == "f" }) {
		t.Errorf("Complete has no f: %v, %v", len(items), err)
	}
}

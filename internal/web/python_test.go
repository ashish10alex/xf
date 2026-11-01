package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/pyrefly"
	"github.com/ashish10alex/xf/internal/pyrefly/pyreflytest"
)

func TestMain(m *testing.M) {
	pyreflytest.Main()
	os.Exit(m.Run())
}

// newPythonTest serves a project of files whose pyrefly is the fake one
// (pyreflytest), with no Python on PATH, and answers with its root, its
// state and the events its pages get.
func newPythonTest(t *testing.T, files map[string]string) (string, *projectState, chan event, string, *http.Client) {
	t.Helper()
	t.Setenv(pyreflytest.Env, "1")
	root := t.TempDir()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})
	p := s.only()
	p.py.lookPath = func(name string) (string, error) {
		if name == "pyrefly" {
			return os.Args[0], nil
		}
		return "", exec.ErrNotFound
	}
	events, ok := p.hub.subscribe()
	if !ok {
		t.Fatal("no events")
	}
	t.Cleanup(func() { p.hub.unsubscribe(events) })
	return root, p, events, ts.URL, c
}

// pythonEventFor waits for a "python" event about file that ok accepts.
func pythonEventFor(t *testing.T, events chan event, file string, ok func(pythonEvent) bool) pythonEvent {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if e.name != "python" {
				continue
			}
			var v pythonEvent
			if err := json.Unmarshal(e.data, &v); err != nil {
				t.Fatal(err)
			}
			if v.File == file && ok(v) {
				return v
			}
		case <-timeout:
			t.Fatalf("no such python event for %s", file)
		}
	}
}

func TestPython(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "lib.py")
	if err := os.WriteFile(lib, []byte("def lib(): ...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(pyreflytest.LibEnv, lib)
	root, p, events, u, c := newPythonTest(t, map[string]string{
		"scripts/a.py":             "import x\nBAD = lib\n",
		".venv/bin/python":         "",
		".venv/Scripts/python.exe": "", // the layout on Windows
		".venv/pyvenv.cfg":         "home = /usr\n",
		".venv/lib/python3.12/site-packages/pkg/__init__.py": "X = 1\n",
		"other/bin/python": "",
		"README.md":        "hi\n",
	})
	venvPy := filepath.Join(root, ".venv", "bin", "python")
	if runtime.GOOS == "windows" {
		venvPy = filepath.Join(root, ".venv", "Scripts", "python.exe")
	}

	var st pythonStatusView
	getJSON(t, c, u+"/api/python/status?file=scripts/a.py", &st)
	if st.Off || st.Missing != "" || st.Interpreter != venvPy || st.Where != ".venv" || st.Config != "" || !st.Offer {
		t.Errorf("status = %+v", st)
	}

	text := "import x\nBAD = lib\n"
	var sync syncView
	postJSON(t, c, u+"/api/python/sync", map[string]any{"file": "scripts/a.py", "text": text}, &sync)
	if sync.Version != 1 {
		t.Errorf("sync = %+v", sync)
	}
	ev := pythonEventFor(t, events, "scripts/a.py", func(v pythonEvent) bool {
		return len(v.Diagnostics) == 1 && strings.Contains(v.Diagnostics[0].Message, venvPy)
	})
	if ev.Version != 1 || ev.Diagnostics[0].Range.Start != (pyrefly.Position{Line: 1, Character: 0}) {
		t.Errorf("event = %+v", ev)
	}
	// The same text again has its errors at once.
	postJSON(t, c, u+"/api/python/sync", map[string]any{"file": "scripts/a.py", "text": text}, &sync)
	if sync.Version != 1 || len(sync.Diagnostics) != 1 {
		t.Errorf("sync again = %+v", sync)
	}

	if code := getStatus(t, c, u+"/api/source?file="+url.QueryEscape(filepath.ToSlash(lib))); code != http.StatusForbidden {
		t.Errorf("a library file before pyrefly named it: %d", code)
	}
	var h hoverView
	postJSON(t, c, u+"/api/python/hover", map[string]any{"file": "scripts/a.py", "text": text, "pos": map[string]int{"line": 0, "character": 8}}, &h)
	if !strings.HasPrefix(h.Markdown, "```python\nx\n```") || h.Version != 1 {
		t.Errorf("hover = %+v", h)
	}
	if l := h.Links[pyrefly.URI(lib)+"#L1,5"]; l != (pyLocationView{File: filepath.ToSlash(lib), Library: true, Line: 0, Col: 4}) {
		t.Errorf("hover's link = %+v (links %+v)", l, h.Links)
	}
	var items struct {
		Items []pyrefly.Completion `json:"items"`
	}
	postJSON(t, c, u+"/api/python/complete", map[string]any{"file": "scripts/a.py", "text": text, "pos": map[string]int{"line": 1}}, &items)
	if len(items.Items) != 2 {
		t.Errorf("completions = %+v", items)
	}
	var sig struct {
		Signature *pyrefly.Signature `json:"signature"`
	}
	postJSON(t, c, u+"/api/python/signature", map[string]any{"file": "scripts/a.py", "text": text, "pos": map[string]int{"line": 1}}, &sig)
	if sig.Signature == nil || sig.Signature.Active != 1 {
		t.Errorf("signature = %+v", sig)
	}

	// gd in the file, and to a library file, which the pane may then show
	// but not save.
	var defs struct {
		Locations []pyLocationView `json:"locations"`
	}
	postJSON(t, c, u+"/api/python/definition", map[string]any{"file": "scripts/a.py", "text": text, "pos": map[string]int{"line": 0, "character": 2}}, &defs)
	if len(defs.Locations) != 1 || defs.Locations[0] != (pyLocationView{File: "scripts/a.py", Line: 0, Col: 4}) {
		t.Errorf("definition = %+v", defs)
	}
	postJSON(t, c, u+"/api/python/definition", map[string]any{"file": "scripts/a.py", "text": text, "pos": map[string]int{"line": 1, "character": 7}}, &defs)
	want := pyLocationView{File: filepath.ToSlash(lib), Library: true, Line: 0, Col: 4}
	if len(defs.Locations) != 1 || defs.Locations[0] != want {
		t.Fatalf("definition = %+v, want %+v", defs, want)
	}
	var v sourceView
	getJSON(t, c, u+"/api/source?file="+url.QueryEscape(want.File), &v)
	if v.Text != "def lib(): ...\n" || v.Lang != "python" || !strings.Contains(v.ReadOnly, "library") || v.Base != nil {
		t.Errorf("library file = %+v", v)
	}
	if code := postStatus(t, c, u+"/api/source/save", map[string]any{"file": want.File, "text": "x", "hash": v.Hash}, nil); code == http.StatusOK {
		t.Error("a library file was saved")
	}
	// A library file is a Python file pyrefly checks too.
	postJSON(t, c, u+"/api/python/sync", map[string]any{"file": want.File, "text": "BAD\n"}, &sync)
	pythonEventFor(t, events, want.File, func(v pythonEvent) bool { return len(v.Diagnostics) == 1 })

	// A virtual environment's files in the project are read-only.
	getJSON(t, c, u+"/api/source?file=.venv/lib/python3.12/site-packages/pkg/__init__.py", &v)
	if !strings.Contains(v.ReadOnly, "library") {
		t.Errorf("a venv's file isn't read-only: %+v", v)
	}
	v = sourceView{}
	getJSON(t, c, u+"/api/source?file=scripts/a.py", &v)
	if v.ReadOnly != "" {
		t.Errorf("the project's file is read-only: %q", v.ReadOnly)
	}

	// Another interpreter: pyrefly asks again, without starting again.
	running := p.py.client
	other := filepath.Join(root, "other", "bin", "python")
	postJSON(t, c, u+"/api/python/interpreter", map[string]any{"path": "other/bin/python"}, &st)
	if st.Interpreter != other || st.Where != "picked" {
		t.Errorf("status after picking = %+v", st)
	}
	pythonEventFor(t, events, "scripts/a.py", func(v pythonEvent) bool {
		return len(v.Diagnostics) == 1 && strings.Contains(v.Diagnostics[0].Message, other)
	})
	if p.py.client != running {
		t.Error("picking an interpreter started pyrefly again")
	}
	if p.sess.Python != other {
		t.Errorf("session's interpreter = %q", p.sess.Python)
	}
	var list struct {
		Current string                `json:"current"`
		List    []pyrefly.Interpreter `json:"list"`
	}
	getJSON(t, c, u+"/api/python/interpreters", &list)
	if list.Current != other || len(list.List) != 2 || list.List[0].Path != other || list.List[1].Where != ".venv" {
		t.Errorf("interpreters = %+v", list)
	}
	if code := postStatus(t, c, u+"/api/python/interpreter", map[string]any{"path": "nope/python"}, nil); code == http.StatusOK {
		t.Error("a missing interpreter was picked")
	}
	postJSON(t, c, u+"/api/python/interpreter", map[string]any{"path": ""}, &st)
	if st.Interpreter != venvPy {
		t.Errorf("back to the found one: %+v", st)
	}

	// The config: written once, then pyrefly reads it.
	var ok okView
	postJSON(t, c, u+"/api/python/config", map[string]any{"create": true}, &ok)
	if got := readFile(t, filepath.Join(root, "pyrefly.toml")); got != pyrefly.ConfigText {
		t.Errorf("pyrefly.toml = %q", got)
	}
	if p.py.client != nil {
		t.Error("pyrefly didn't stop to read the new config")
	}
	st = pythonStatusView{}
	getJSON(t, c, u+"/api/python/status?file=scripts/a.py", &st)
	if st.Config != "pyrefly.toml" || st.Offer {
		t.Errorf("status with a config = %+v", st)
	}
	if code := postStatus(t, c, u+"/api/python/config", map[string]any{"create": true}, nil); code == http.StatusOK {
		t.Error("pyrefly.toml was written twice")
	}

	// Only Python files.
	if code := postStatus(t, c, u+"/api/python/sync", map[string]any{"file": "README.md", "text": "hi"}, nil); code != http.StatusForbidden {
		t.Errorf("sync of a Markdown file: %d", code)
	}
	if code := postStatus(t, c, u+"/api/python/sync", map[string]any{"file": "../a.py", "text": "x"}, nil); code != http.StatusForbidden {
		t.Errorf("sync outside the project: %d", code)
	}

	// Off.
	p.mu.Lock()
	p.pythonServer = config.PythonOff
	p.mu.Unlock()
	st = pythonStatusView{}
	getJSON(t, c, u+"/api/python/status?file=scripts/a.py", &st)
	if !st.Off {
		t.Errorf("status when off = %+v", st)
	}
	if code := postStatus(t, c, u+"/api/python/sync", map[string]any{"file": "scripts/a.py", "text": text}, nil); code != http.StatusServiceUnavailable {
		t.Errorf("sync when off: %d", code)
	}
}

func TestPythonDeclineAndMissing(t *testing.T) {
	_, p, _, u, c := newPythonTest(t, map[string]string{"a.py": "x = 1\n"})
	var ok okView
	postJSON(t, c, u+"/api/python/config", map[string]any{"create": false}, &ok)
	var st pythonStatusView
	getJSON(t, c, u+"/api/python/status?file=a.py", &st)
	if st.Offer || !p.sess.PyreflyDeclined {
		t.Errorf("status after no = %+v", st)
	}
	p.py.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	st = pythonStatusView{}
	getJSON(t, c, u+"/api/python/status?file=a.py", &st)
	if !strings.Contains(st.Missing, "uv tool install pyrefly") || st.Interpreter != "" {
		t.Errorf("status without pyrefly = %+v", st)
	}
	var e errorView
	if code := postStatus(t, c, u+"/api/python/sync", map[string]any{"file": "a.py", "text": "x"}, &e); code != http.StatusServiceUnavailable || !strings.Contains(e.Error, "uv tool install") {
		t.Errorf("sync without pyrefly: %d %+v", code, e)
	}
}

func TestPythonStopsWithTheProject(t *testing.T) {
	_, p, _, u, c := newPythonTest(t, map[string]string{"a.py": "x = 1\n"})
	var sync syncView
	postJSON(t, c, u+"/api/python/sync", map[string]any{"file": "a.py", "text": "x = 1\n"}, &sync)
	client := p.py.client
	if client == nil {
		t.Fatal("pyrefly didn't start")
	}
	p.close()
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("pyrefly outlived its project")
	}
}

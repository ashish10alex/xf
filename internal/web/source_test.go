package web

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// newSourceTest serves testGraph's project from a temporary root holding
// files, by path relative to the root.
func newSourceTest(t *testing.T, files map[string]string) (string, *httptest.Server, *http.Client) {
	t.Helper()
	root := t.TempDir()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})
	return root, ts, c
}

// postStatus posts body and decodes the answer into v whatever its status,
// which it returns.
func postStatus(t *testing.T, c *http.Client, u string, body, v any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	res, err := c.Post(u, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if v != nil && res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusBadRequest {
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return res.StatusCode
}

func getStatus(t *testing.T, c *http.Client, u string) int {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSourceSave(t *testing.T) {
	root, ts, c := newSourceTest(t, map[string]string{"definitions/orders.sqlx": "config { type: \"table\" }\nSELECT 1\n"})
	path := filepath.Join(root, "definitions", "orders.sqlx")

	var v sourceView
	getJSON(t, c, ts.URL+"/api/source?file=definitions/orders.sqlx", &v)
	if v.Text != "config { type: \"table\" }\nSELECT 1\n" || v.Lang != "sqlx" || v.ReadOnly != "" || v.Hash == "" {
		t.Fatalf("source = %+v", v)
	}

	var saved sourceView
	postJSON(t, c, ts.URL+"/api/source/save", map[string]string{"file": v.File, "text": "SELECT 2\n", "hash": v.Hash}, &saved)
	if got := readFile(t, path); got != "SELECT 2\n" {
		t.Fatalf("file = %q", got)
	}
	if saved.Hash == v.Hash || saved.Text != "SELECT 2\n" {
		t.Fatalf("saved = %+v", saved)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
			t.Errorf("mode = %v, want 0640 kept", fi.Mode().Perm())
		}
	}

	// A save from the text loaded before is refused: the file changed.
	var conflict conflictView
	if st := postStatus(t, c, ts.URL+"/api/source/save", map[string]string{"file": v.File, "text": "SELECT 3\n", "hash": v.Hash}, &conflict); st != http.StatusConflict {
		t.Fatalf("stale save: status %d, want 409", st)
	}
	if conflict.Kind != "conflict" || conflict.Current.Text != "SELECT 2\n" || conflict.Current.Hash != saved.Hash {
		t.Fatalf("conflict = %+v", conflict)
	}
	if got := readFile(t, path); got != "SELECT 2\n" {
		t.Fatalf("a refused save wrote: %q", got)
	}

	// Another program changes it: the pane's hash no longer matches.
	if err := os.WriteFile(path, []byte("SELECT 4\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if st := postStatus(t, c, ts.URL+"/api/source/save", map[string]string{"file": v.File, "text": "SELECT 5\n", "hash": saved.Hash}, &conflict); st != http.StatusConflict {
		t.Fatalf("save over an outside change: status %d, want 409", st)
	}
	// Overwriting is a save with the hash of what is on disk now.
	postJSON(t, c, ts.URL+"/api/source/save", map[string]string{"file": v.File, "text": "SELECT 5\n", "hash": conflict.Current.Hash}, nil)
	if got := readFile(t, path); got != "SELECT 5\n" {
		t.Fatalf("overwrite: file = %q", got)
	}

	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("files left beside it: %v", entries)
	}
}

func TestSourceKeepsHowTheFileIsWritten(t *testing.T) {
	files := map[string]string{
		"definitions/orders.sqlx":    "\xEF\xBB\xBFconfig {}\r\nSELECT 1\r\n",
		"definitions/customers.sqlx": "SELECT 1 AS id",
		"definitions/sources.js":     "declare({})\n",
	}
	root, ts, c := newSourceTest(t, files)
	for name, text := range files {
		var v sourceView
		getJSON(t, c, ts.URL+"/api/source?file="+name, &v)
		if strings.Contains(v.Text, "\r") || strings.HasPrefix(v.Text, "\xEF\xBB\xBF") {
			t.Errorf("%s: text %q has CR or BOM", name, v.Text)
		}
		postJSON(t, c, ts.URL+"/api/source/save", map[string]string{"file": name, "text": v.Text, "hash": v.Hash}, nil)
		if got := readFile(t, filepath.Join(root, filepath.FromSlash(name))); got != text {
			t.Errorf("%s after an unchanged save = %q, want %q", name, got, text)
		}
	}

	// An edit is written with the file's CRLF line ends and its BOM.
	var v sourceView
	getJSON(t, c, ts.URL+"/api/source?file=definitions/orders.sqlx", &v)
	postJSON(t, c, ts.URL+"/api/source/save", map[string]string{"file": v.File, "text": "config {}\nSELECT 2\n", "hash": v.Hash}, nil)
	if got := readFile(t, filepath.Join(root, "definitions", "orders.sqlx")); got != "\xEF\xBB\xBFconfig {}\r\nSELECT 2\r\n" {
		t.Errorf("edited CRLF file = %q", got)
	}
}

func TestSourceMixedLineEndsAreReadOnly(t *testing.T) {
	_, ts, c := newSourceTest(t, map[string]string{"definitions/orders.sqlx": "a\r\nb\nc\n"})
	var v sourceView
	getJSON(t, c, ts.URL+"/api/source?file=definitions/orders.sqlx", &v)
	if v.ReadOnly == "" {
		t.Fatal("a file with mixed line ends can be saved")
	}
	var e errorView
	if st := postStatus(t, c, ts.URL+"/api/source/save", map[string]string{"file": v.File, "text": "x", "hash": v.Hash}, &e); st == http.StatusOK {
		t.Fatal("saved a file with mixed line ends")
	}
}

// Any file in the project opens, an action's or not; nothing outside it,
// through .. or a link, and nothing in .git.
func TestSourceOnlyProjectFiles(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.sqlx"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, ts, c := newSourceTest(t, map[string]string{
		"definitions/orders.sqlx":    "SELECT 1\n",
		"includes/constants.js":      "module.exports = {}\n",
		"workflow_settings.yaml":     "defaultProject: p\n",
		"definitions/notanaction.js": "x\n",
		".git/config":                "[core]\n",
		"data/logo.png":              "\x89PNG\x00\x00",
	})
	// customers.sqlx is an action's file, but a link out of the project.
	if err := os.Symlink(filepath.Join(outside, "secret.sqlx"), filepath.Join(root, "definitions", "customers.sqlx")); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"includes/constants.js", "workflow_settings.yaml", "definitions/notanaction.js"} {
		var v sourceView
		getJSON(t, c, ts.URL+"/api/source?file="+file, &v)
		postJSON(t, c, ts.URL+"/api/source/save", map[string]string{"file": file, "text": "edited\n", "hash": v.Hash}, nil)
		if got := readFile(t, filepath.Join(root, filepath.FromSlash(file))); got != "edited\n" {
			t.Errorf("%s after a save = %q", file, got)
		}
	}
	for _, file := range []string{
		"../" + filepath.Base(outside) + "/secret.sqlx", "definitions/../definitions/orders.sqlx",
		"definitions/customers.sqlx", // the symlink
		".git/config", ".GIT/config", "/etc/passwd", "", "definitions", "definitions/",
	} {
		if st := getStatus(t, c, ts.URL+"/api/source?file="+file); st != http.StatusForbidden {
			t.Errorf("GET %q: status %d, want 403", file, st)
		}
		if st := postStatus(t, c, ts.URL+"/api/source/save", map[string]string{"file": file, "text": "x", "hash": hashOf([]byte("secret"))}, nil); st != http.StatusForbidden {
			t.Errorf("save %q: status %d, want 403", file, st)
		}
	}
	if got := readFile(t, filepath.Join(outside, "secret.sqlx")); got != "secret" {
		t.Fatalf("wrote outside the project: %q", got)
	}
	if st := getStatus(t, c, ts.URL+"/api/source?file=data/logo.png"); st != http.StatusUnprocessableEntity {
		t.Errorf("a binary file: status %d, want 422", st)
	}
	// A file that doesn't exist yet isn't made by a save.
	if st := getStatus(t, c, ts.URL+"/api/source?file=definitions/sources.js"); st == http.StatusOK {
		t.Error("read a file that doesn't exist")
	}
	if st := postStatus(t, c, ts.URL+"/api/source/save", map[string]string{"file": "definitions/sources.js", "text": "x", "hash": ""}, nil); st == http.StatusOK {
		t.Error("made a new file")
	}
	if _, err := os.Stat(filepath.Join(root, "definitions", "sources.js")); err == nil {
		t.Error("definitions/sources.js was made")
	}
}

func TestSourceCompleteAndRefs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "definitions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "definitions", "orders.sqlx"), []byte("SELECT 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A compile names each action's table as refs name it.
	g := testGraph()
	for _, a := range g.Actions {
		a.CanonicalTarget = a.Target
	}
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(g)})
	// Unsaved text: what the editor holds, not what is on disk. é makes
	// UTF-16 and byte offsets differ.
	text := "-- é\nSELECT * FROM ${ref(\"customers\")} AS c JOIN $cus"
	var comp struct{ Items []completionView }
	postJSON(t, c, ts.URL+"/api/source/complete", map[string]any{"file": "definitions/orders.sqlx", "text": text, "offset": len([]rune(text))}, &comp)
	var labels []string
	for _, it := range comp.Items {
		labels = append(labels, it.Label)
		if it.From != strings.LastIndex(text, "$")-1 { // é is 2 bytes, 1 unit
			t.Errorf("%s: from %d", it.Label, it.From)
		}
	}
	if !slices.Contains(labels, "customers") || !slices.Contains(labels, "orders") {
		t.Fatalf("completions = %v", labels)
	}
	for _, it := range comp.Items {
		if it.Label == "customers" && it.Insert != `${ref("customers")}` {
			t.Errorf("customers inserts %q", it.Insert)
		}
	}

	var refs struct {
		Refs []refView
		JS   [][2]int
	}
	postJSON(t, c, ts.URL+"/api/source/refs", map[string]any{"file": "definitions/orders.sqlx", "text": text}, &refs)
	if len(refs.Refs) != 1 {
		t.Fatalf("refs = %+v", refs.Refs)
	}
	r := refs.Refs[0]
	units := []rune(text) // these are all one unit each
	if got := string(units[r.From:r.To]); got != `ref("customers")` || r.Project != "p" || r.Dataset != "mart" || r.Table != "customers" || r.Action != "p.mart.customers" || !r.HasTable {
		t.Errorf("ref = %+v covering %q", r, got)
	}
	if len(refs.JS) != 1 || string(units[refs.JS[0][0]:refs.JS[0][1]]) != `ref("customers")` {
		t.Errorf("js spans = %v", refs.JS)
	}

	// Only an action's file.
	if st := postStatus(t, c, ts.URL+"/api/source/refs", map[string]any{"file": "includes/x.js", "text": text}, nil); st != http.StatusForbidden {
		t.Errorf("refs of a non-action file: status %d", st)
	}
}

// An operation with hasOutput has a table, and a schema to show; one
// without has none, and says what it is so the hover can say why.
func TestSourceRefsOfOperations(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "definitions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "definitions", "orders.sqlx"), []byte("SELECT 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := project.Target{Database: "p", Schema: "mart", Name: "calendar"}
	ops := project.Target{Database: "p", Schema: "mart", Name: "cleanup"}
	actions := slices.Collect(maps.Values(testGraph().Actions))
	actions = append(actions,
		&project.Action{ID: out.ID(), Target: out, Kind: project.KindOperation, HasOutput: true, FileName: "definitions/calendar.sqlx"},
		&project.Action{ID: ops.ID(), Target: ops, Kind: project.KindOperation, FileName: "definitions/cleanup.sqlx"})
	for _, a := range actions {
		a.CanonicalTarget = a.Target
	}
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(project.NewGraph(actions))})
	var refs struct{ Refs []refView }
	postJSON(t, c, ts.URL+"/api/source/refs", map[string]any{"file": "definitions/orders.sqlx",
		"text": `SELECT * FROM ${ref("calendar")}, ${ref("cleanup")}`}, &refs)
	got := map[string]refView{}
	for _, r := range refs.Refs {
		got[r.Table] = r
	}
	if r := got["calendar"]; !r.HasTable || r.Kind != "operation" {
		t.Errorf("operation with output: %+v", r)
	}
	if r := got["cleanup"]; r.HasTable || r.Kind != "operation" || r.ConsoleURL != "" {
		t.Errorf("operation without output: %+v", r)
	}
	if r := got["calendar"]; r.ConsoleURL != out.ConsoleURL() {
		t.Errorf("operation with output: console URL %q, want %q", r.ConsoleURL, out.ConsoleURL())
	}
}

func TestSourceDiagnostics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "definitions", "orders.sqlx")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// The file is the table's compiled query, so a dry run's [line:column]
	// lands on the same place in it.
	if err := os.WriteFile(path, []byte("SELECT id\nFROM `p.raw.orders`"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := newFakeEngine(testGraph())
	dry := func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
		if strings.Contains(sql, "SELECT id") {
			return nil, &bigquery.APIError{Status: 400, Message: "Unrecognized name: id at [1:8]"}
		}
		return &bigquery.DryRun{}, nil
	}
	s, ts, c := newTest(t, frontend.Deps{Root: root, Engine: eng, DryRun: dry})

	var v struct {
		Hash        string
		Diagnostics []diagnosticView
	}
	getJSON(t, c, ts.URL+"/api/source/diagnostics?file=definitions/orders.sqlx", &v)
	if len(v.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v", v.Diagnostics)
	}
	d := v.Diagnostics[0]
	if d.Line != 0 || d.Start != 7 || d.End != 9 || d.Severity != "error" || !strings.Contains(d.Message, "Unrecognized name: id") {
		t.Errorf("diagnostic = %+v", d)
	}
	if v.Hash != hashOf([]byte("SELECT id\nFROM `p.raw.orders`")) {
		t.Errorf("hash = %s", v.Hash)
	}

	// A compile error in the file is shown instead of dry runs.
	g := testGraph()
	eng.states <- engine.State{Graph: g, Compiles: 2, Errors: []project.CompilationError{{FileName: "definitions/orders.sqlx", Message: "Unexpected token"}}}
	waitFor(t, s.only().hasErrors)
	getJSON(t, c, ts.URL+"/api/source/diagnostics?file=definitions/orders.sqlx", &v)
	if len(v.Diagnostics) != 1 || !strings.Contains(v.Diagnostics[0].Message, "Unexpected token") || v.Diagnostics[0].Source != "dataform compile" {
		t.Errorf("diagnostics with a compile error = %+v", v.Diagnostics)
	}
}

// A dbt model's dry-run errors are placed through its Jinja: a missing
// table lands on the ref() that named it.
func TestSourceDiagnosticsDbt(t *testing.T) {
	root := t.TempDir()
	src := "{{ config(materialized='table') }}\n\nselect id\nfrom {{ ref('orders') }}"
	path := filepath.Join(root, "models", "m.sql")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m := project.Target{Database: "p", Schema: "d", Name: "m"}
	a := &project.Action{ID: m.ID(), Target: m, CanonicalTarget: m, Kind: project.KindTable, FileName: "models/m.sql", RawOps: true,
		Query: "\n\nselect id\nfrom `p`.`d`.`orders`"}
	dry := func(ctx context.Context, project, location, sql string) (*bigquery.DryRun, error) {
		return nil, &bigquery.APIError{Status: 404, Message: "Not found: Table p:d.orders was not found in location EU"}
	}
	s, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(project.NewGraph([]*project.Action{a})), DryRun: dry})
	s.only().tool = "dbt"
	var v struct{ Diagnostics []diagnosticView }
	getJSON(t, c, ts.URL+"/api/source/diagnostics?file=models/m.sql", &v)
	if len(v.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v", v.Diagnostics)
	}
	if d := v.Diagnostics[0]; d.Line != 3 || d.Start != 5 || d.End != 24 || d.Source != "bigquery dry run" {
		t.Errorf("diagnostic = %+v", d)
	}
}

func TestSourceAutosaveFloor(t *testing.T) {
	for _, tt := range []struct{ set, want int }{{0, 0}, {-1, 0}, {100, 500}, {1500, 1500}} {
		s, ts, c := newTestProject(t, &Project{Deps: frontend.Deps{Engine: newFakeEngine(testGraph())}, SourceAutosaveMs: tt.set})
		var boot projectBoot
		getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
		if boot.SourceAutosaveMs != tt.want {
			t.Errorf("sourceAutosaveMs %d: page gets %d, want %d", tt.set, boot.SourceAutosaveMs, tt.want)
		}
	}
}

func TestSourceDefinition(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"definitions/orders.sqlx":    "SELECT 1\n",
		"definitions/customers.sqlx": "SELECT 1 AS id\n",
		"definitions/sources.js":     "// é\ndeclare({ schema: \"raw\", name: \"orders\" });\n",
	}
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g := testGraph()
	for _, a := range g.Actions {
		a.CanonicalTarget = a.Target
	}
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(g)})
	// Unsaved text; é makes UTF-16 and byte columns differ.
	text := "/* é */ WITH x AS (SELECT 1), base AS (SELECT * FROM ${ref(\"customers\")} JOIN ${ref(\"raw\", \"orders\")})\nSELECT * FROM base"
	units := []rune(text)
	def := func(at string, nth int) []locationView {
		t.Helper()
		i := 0
		for range nth {
			i += strings.Index(string(units[i:]), at) + 1
		}
		var res struct{ Locations []locationView }
		postJSON(t, c, ts.URL+"/api/source/definition", map[string]any{"file": "definitions/orders.sqlx", "text": text, "offset": i}, &res)
		return res.Locations
	}

	if got := def("customers", 1); len(got) != 1 || got[0] != (locationView{File: "definitions/customers.sqlx", Action: "p.mart.customers"}) {
		t.Errorf("ref(customers) = %+v", got)
	}
	// A declaration in a JavaScript file: the line naming it, after é.
	if got := def("\"orders", 1); len(got) != 1 || got[0] != (locationView{File: "definitions/sources.js", Line: 1, Col: 32, Action: "p.raw.orders"}) {
		t.Errorf("ref(raw, orders) = %+v", got)
	}
	// A CTE in the unsaved text, in UTF-16 columns.
	if got := def("base", 2); len(got) != 1 || got[0] != (locationView{File: "definitions/orders.sqlx", Line: 0, Col: len([]rune(text[:strings.Index(text, "base")])), Action: "p.mart.orders"}) {
		t.Errorf("CTE base = %+v", got)
	}
	if got := def("SELECT", 1); len(got) != 0 {
		t.Errorf("a keyword = %+v", got)
	}
}

func TestSourceInclude(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"definitions/orders.sqlx": "SELECT 1\n",
		"includes/parameters.js":  "// é first.\nconst START = \"2025-03-29\";\nmodule.exports = { START };\n",
	}
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})
	// é before the island makes UTF-16 and byte offsets differ.
	text := "-- é\nSELECT '${parameters.START}'"
	at := len([]rune(text[:strings.Index(text, "START")])) + 2
	var res struct{ Include *includeView }
	postJSON(t, c, ts.URL+"/api/source/include", map[string]any{"file": "definitions/orders.sqlx", "text": text, "offset": at}, &res)
	want := includeView{From: at - 2 - len("parameters."), To: at - 2 + len("START"), Code: `const START = "2025-03-29"`, Doc: "é first.",
		locationView: locationView{File: "includes/parameters.js", Line: 1, Col: 6}}
	if res.Include == nil || *res.Include != want {
		t.Fatalf("got %+v, want %+v", res.Include, want)
	}
	res.Include = nil
	postJSON(t, c, ts.URL+"/api/source/include", map[string]any{"file": "definitions/orders.sqlx", "text": text, "offset": 3}, &res)
	if res.Include != nil {
		t.Errorf("off a name: %+v", res.Include)
	}
}

// TestSourceConfigKey answers a config key's hover, in UTF-16 offsets.
func TestSourceConfigKey(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "definitions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "definitions", "orders.sqlx"), []byte("SELECT 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(testGraph())})
	text := "config {\n  description: \"é\",\n  type: \"view\",\n  uniqueKey: [\"id\"]\n}"
	at := len([]rune(text[:strings.Index(text, "uniqueKey")])) + 3
	var res struct{ Key *configKeyView }
	postJSON(t, c, ts.URL+"/api/source/configkey", map[string]any{"file": "definitions/orders.sqlx", "text": text, "offset": at}, &res)
	k := res.Key
	if k == nil || k.Path != "uniqueKey" || k.From != at-3 || k.To != at-3+len("uniqueKey") || k.ConfigType != "view" || !k.Unread || k.Doc == "" {
		t.Fatalf("got %+v", k)
	}
	res.Key = nil
	postJSON(t, c, ts.URL+"/api/source/configkey", map[string]any{"file": "definitions/orders.sqlx", "text": text, "offset": 2}, &res)
	if res.Key != nil {
		t.Errorf("off a key: %+v", res.Key)
	}
}

func TestSourceSymbols(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"definitions/orders.sqlx": "SELECT 1\n",
	}
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g := testGraph()
	_, ts, c := newTest(t, frontend.Deps{Root: root, Engine: newFakeEngine(g)})

	// Unsaved text with CTEs, refs, and a literal BigQuery table
	text := "WITH base AS (\n  SELECT * FROM ${ref(\"customers\")}\n  JOIN `p.raw.orders` ON true\n)\nSELECT * FROM base\n"
	var res struct{ Symbols []symbolView }
	postJSON(t, c, ts.URL+"/api/source/symbols", map[string]any{"file": "definitions/orders.sqlx", "text": text}, &res)

	if len(res.Symbols) != 3 {
		t.Fatalf("symbols count = %d, want 3; got: %+v", len(res.Symbols), res.Symbols)
	}

	// ReferencesFirst: refs and tables first, then CTEs
	wantKinds := []string{"ref", "bq_table", "cte"}
	for i, w := range wantKinds {
		if res.Symbols[i].Kind != w {
			t.Errorf("symbol[%d].Kind = %q, want %q", i, res.Symbols[i].Kind, w)
		}
	}

	if res.Symbols[0].Name != `ref("customers")` || res.Symbols[0].Line != 2 {
		t.Errorf("symbol[0] = %+v", res.Symbols[0])
	}
	if res.Symbols[1].Name != "p.raw.orders" || res.Symbols[1].Line != 3 {
		t.Errorf("symbol[1] = %+v", res.Symbols[1])
	}
	if res.Symbols[2].Name != "base" || res.Symbols[2].Line != 1 {
		t.Errorf("symbol[2] = %+v", res.Symbols[2])
	}

	// Non-action file is forbidden
	if st := postStatus(t, c, ts.URL+"/api/source/symbols", map[string]any{"file": "includes/x.js", "text": "SELECT 1"}, nil); st != http.StatusForbidden {
		t.Errorf("symbols of a non-action file: status %d", st)
	}
}

// The source view's base is the file as git's index has it: what the pane
// marks the changes since.
func TestSourceGitBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if gitBase(filepath.Join(t.TempDir(), "x.sqlx")) != nil {
		t.Error("a file outside a repository has a base")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, text string) string {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	git("init", "-q")
	tracked := write("definitions/orders.sqlx", "SELECT 1\r\nFROM a\r\n")
	write(".gitignore", "ignored.sqlx\n")
	git("add", "definitions/orders.sqlx", ".gitignore")
	write("definitions/orders.sqlx", "SELECT 2\r\nFROM a\r\n")

	if b := gitBase(tracked); b == nil || *b != "SELECT 1\nFROM a\n" {
		t.Errorf("tracked file's base = %v, want the index's text with \\n line ends", b)
	}
	if b := gitBase(write("definitions/new.sqlx", "SELECT 3\n")); b == nil || *b != "" {
		t.Errorf("untracked file's base = %v, want empty", b)
	}
	if b := gitBase(write("ignored.sqlx", "SELECT 4\n")); b != nil {
		t.Errorf("ignored file's base = %q, want none", *b)
	}
}

// settleIndex makes root's index older than baseSettle, as an index is
// when its files' bases may be kept.
func settleIndex(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, ".git", "index"), old, old); err != nil {
		t.Fatal(err)
	}
}

// A base kept from the index goes when the index is rewritten: staging the
// file shows its new text.
func TestSourceGitBaseKeptUntilIndexChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	path := filepath.Join(root, "orders.sqlx")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("SELECT 1\n")
	git("add", "orders.sqlx")
	settleIndex(t, root)
	for range 2 {
		if b := gitBase(path); b == nil || *b != "SELECT 1\n" {
			t.Fatalf("base = %v, want SELECT 1", b)
		}
	}
	write("SELECT 2\n")
	git("add", "orders.sqlx")
	if b := gitBase(path); b == nil || *b != "SELECT 2\n" {
		t.Errorf("base after git add = %v, want SELECT 2", b)
	}
	settleIndex(t, root)
	if b := gitBase(path); b == nil || *b != "SELECT 2\n" {
		t.Errorf("base after the index settled = %v, want SELECT 2", b)
	}
}

// Reading bases leaves .git/index alone, even when a file's stat data is
// stale, as after a save: an index.lock taken while Dataform copies the
// project stops the compile.
func TestSourceGitBaseLeavesIndexAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	tracked, untracked := filepath.Join(root, "orders.sqlx"), filepath.Join(root, "new.sqlx")
	for _, p := range []string{tracked, untracked} {
		if err := os.WriteFile(p, []byte("SELECT 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("add", "orders.sqlx")
	index := filepath.Join(root, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	later := before.ModTime().Add(5 * time.Second)
	if err := os.Chtimes(tracked, later, later); err != nil {
		t.Fatal(err)
	}
	gitBase(tracked)
	gitBase(untracked)
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Errorf(".git/index was rewritten: %v -> %v", before.ModTime(), after.ModTime())
	}
}

// indexOf finds the index as git does: in the nearest .git folder, or in
// the git dir a .git file names.
func TestIndexOf(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) string {
		t.Helper()
		p = filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mk("repo/.git")
	mk("repo/.git/worktrees/wt")
	sub := mk("repo/definitions/sub")
	wt := mk("wt/definitions")
	if err := os.WriteFile(filepath.Join(root, "wt", ".git"), []byte("gitdir: ../repo/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, want string }{
		{sub, filepath.Join(root, "repo", ".git", "index")},
		{wt, filepath.Join(root, "repo", ".git", "worktrees", "wt", "index")},
		{mk("none"), ""},
	} {
		got := indexOf(c.dir)
		// With none in root, a repository the temp dir is in is git's too.
		if c.want == "" && !strings.HasPrefix(got, root) {
			continue
		}
		if got != c.want {
			t.Errorf("indexOf(%s) = %q, want %q", c.dir, got, c.want)
		}
	}
}

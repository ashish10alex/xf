package diagnose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

func testdata(t *testing.T, parts ...string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func loadGraph(t *testing.T, fixture string) *project.Graph {
	t.Helper()
	data, err := os.ReadFile(testdata(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	if data, err = dataform.ExtractCompileJSON(data); err != nil {
		t.Fatal(err)
	}
	g, err := dataform.ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func testProject(t *testing.T, name string) navigate.Project {
	t.Helper()
	return navigate.Project{Root: testdata(t, "projects", name), Graph: loadGraph(t, name+".json")}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// render shows a diagnostic as "line:start-end severity: message [fix]" with
// the marked source text, which keeps expectations readable.
func render(src []byte, d Diagnostic) string {
	sev := "error"
	if d.Severity == Hint {
		sev = "hint"
	}
	text := string(lineText(src, d.Line))
	out := fmt.Sprintf("%d %s %q: %s", d.Line, sev, text[min(d.Start, len(text)):min(d.End, len(text))], d.Message)
	if d.Fix != "" {
		out += " [fix " + d.Fix + "]"
	}
	return out
}

func renderAll(src []byte, ds []Diagnostic) []string {
	var out []string
	for _, d := range ds {
		out = append(out, render(src, d))
	}
	return out
}

// lineOf is the 0-based line of the first occurrence of text in src.
func lineOf(t *testing.T, src []byte, text string) int {
	t.Helper()
	i := strings.Index(string(src), text)
	if i < 0 {
		t.Fatalf("%q not in source", text)
	}
	return strings.Count(string(src[:i]), "\n")
}

// TestCompileDbt puts dbt's errors on the ref() they couldn't resolve:
// dbt v2 names the line, dbt-core only the model.
func TestCompileDbt(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "dbt-broken"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "models", "order_totals.sql")
	for _, fixture := range []string{"dbt-v2-broken.json", "dbt-core-broken.json"} {
		data, err := os.ReadFile(testdata(t, "examples", fixture))
		if err != nil {
			t.Fatal(err)
		}
		g, err := (&dbt.Backend{}).Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		ds := Compile(root, "dbt", g.Errors, os.ReadFile)[file]
		if len(ds) != 1 || ds[0].Line != 3 || ds[0].Source != "dbt compile" {
			t.Fatalf("%s: got %+v", fixture, ds)
		}
		if fixture == "dbt-core-broken.json" && ds[0].Start != len("join {{ ref('") {
			t.Errorf("%s: starts at %d, want the name in ref()", fixture, ds[0].Start)
		}
	}
}

func TestCompile(t *testing.T) {
	root := testdata(t, "projects", "compile_error")
	g := loadGraph(t, "compile_error.json")
	got := Compile(root, "dataform", g.Errors, os.ReadFile)

	missing := filepath.Join(root, "definitions", "missing_ref.sqlx")
	src := read(t, missing)
	want := []string{
		`2 error "does_not_exist": Could not resolve "does_not_exist"`,
	}
	gotMissing := renderAll(src, got[missing])
	if len(gotMissing) < 1 || gotMissing[0] != want[0] {
		t.Errorf("missing_ref.sqlx: got %q", gotMissing)
	}
	for _, s := range gotMissing[1:] {
		if !strings.HasPrefix(s, `2 error "does_not_exist": Missing dependency`) {
			t.Errorf("missing dependency not on the ref: %q", s)
		}
	}

	bad := filepath.Join(root, "definitions", "bad_js.js")
	for _, s := range renderAll(read(t, bad), got[bad]) {
		if !strings.HasPrefix(s, "0 error ") {
			t.Errorf("bad_js.js error not on line 0: %q", s)
		}
	}
	if len(got[bad]) != 2 {
		t.Errorf("bad_js.js has %d diagnostics, want 2", len(got[bad]))
	}
}

// Dataform on Windows names the file with backslashes, in the error and in
// its stack: the errors still land where they do from macOS's output.
func TestCompileWindowsOutput(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "dataform-broken"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "definitions", "order_totals.sqlx")
	src := read(t, file)
	mac := renderAll(src, Compile(root, "dataform", loadGraph(t, "examples/dataform-broken.json").Errors, os.ReadFile)[file])
	win := renderAll(src, Compile(root, "dataform", loadGraph(t, "examples/dataform-broken-windows.json").Errors, os.ReadFile)[file])
	if len(mac) == 0 || !slices.Equal(win, mac) {
		t.Errorf("from Windows' output: %q\nfrom macOS': %q", win, mac)
	}
}

func TestCompileSyntaxErrorStack(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "definitions", "a.sqlx")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "config {\n  type: \"table\",\n  dependencies: [\"x],\n}\nSELECT 1\n"
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	g := &project.Graph{Errors: []project.CompilationError{{
		FileName: "definitions/a.sqlx",
		Message:  "Invalid or unexpected token",
		Stack:    "/tmp/abc/definitions/a.sqlx:3\n  dependencies: [\"x],\n                 ^^^^\n\nSyntaxError: Invalid or unexpected token\n    at new Script (node:vm:117:7)\n    at VMScript._compile (x.js:1:1)",
	}}}
	got := renderAll([]byte(src), Compile(root, "dataform", g.Errors, os.ReadFile)[file])
	want := "2 error \"  dependencies: [\\\"x],\": Invalid or unexpected token\n  dependencies: [\"x],\n                 ^^^^\n\nSyntaxError: Invalid or unexpected token"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if n := Line(root, g.Errors[0], os.ReadFile); n != 3 {
		t.Errorf("Line = %d, want 3", n)
	}
	if s, want := Snippet(g.Errors[0]), "  dependencies: [\"x],\n                 ^^^^\n\nSyntaxError: Invalid or unexpected token"; s != want {
		t.Errorf("Snippet = %q, want %q", s, want)
	}
	if k := Kind(g.Errors[0]); k != KindRoot {
		t.Errorf("Kind = %q, want %q", k, KindRoot)
	}
}

// Dataform's stack names a line of a .sqlx file 7 lines on from the one
// its code quotes: the error goes on the line with that code.
func TestCompileSyntaxErrorStackOffset(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "definitions", "a.sqlx")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "config {\n  type: \"table\",\n  description: \"x\"\n  bigquery: {\n    partitionBy: \"d\"\n  },\n}\nSELECT 1\n\n\n\n\n"
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	e := project.CompilationError{FileName: "definitions/a.sqlx", Message: "Unexpected identifier",
		Stack: "/tmp/abc/definitions/a.sqlx:11\n  bigquery: {\n  ^^^^^^^^\n\nSyntaxError: Unexpected identifier\n    at new Script (node:vm:117:7)"}
	if n := Line(root, e, os.ReadFile); n != 4 {
		t.Errorf("Line = %d, want 4, the line reading bigquery: {", n)
	}
	if ds := Compile(root, "dataform", []project.CompilationError{e}, os.ReadFile)[file]; len(ds) != 1 || ds[0].Line != 3 {
		t.Errorf("diagnostics = %+v, want one on line 3 (0-based)", ds)
	}
}

// The errors tab groups a compile's errors by kind and links each to its
// line: a ref() that resolves to nothing is on the ref's line, and an
// error that names no line has none.
// The CLI asks for dataform install when the project's @dataform/core is
// missing or doesn't match it.
func TestNeedsCoreInstall(t *testing.T) {
	for _, c := range []struct {
		out  string
		want bool
	}{
		{"Error: @dataform/core 0.0.0 is not compatible with @dataform/cli 3.1.2. ... pin @dataform/core in package.json and run `dataform install`.", true},
		{"Could not find a recent installed version of @dataform/core in the project. ... If using `package.json`, then run `dataform install`.", true},
		{"SyntaxError: Unexpected identifier 'SELECT'", false},
	} {
		if got := NeedsCoreInstall(c.out); got != c.want {
			t.Errorf("NeedsCoreInstall(%q) = %v, want %v", c.out, got, c.want)
		}
	}
}

func TestNeedsInitCreds(t *testing.T) {
	if !NeedsInitCreds("Error: Missing credentials JSON file; not found at path '/p/.df-credentials.json'.") {
		t.Error("the missing credentials file isn't seen")
	}
	if NeedsInitCreds("Error: Access Denied") {
		t.Error("another error asks for init-creds")
	}
}

func TestKindAndLine(t *testing.T) {
	root := testdata(t, "projects", "compile_error")
	g := loadGraph(t, "compile_error.json")
	kinds := map[string]int{}
	for _, e := range g.Errors {
		k, n := Kind(e), Line(root, e, os.ReadFile)
		kinds[k]++
		switch {
		case k == KindUnresolved && n != 3:
			t.Errorf("%s: %q on line %d, want 3", e.FileName, e.Message, n)
		case e.FileName == "definitions/bad_js.js" && n != 0:
			t.Errorf("bad_js.js: %q on line %d, want none", e.Message, n)
		}
	}
	if kinds[KindUnresolved] == 0 || kinds[KindMissing] == 0 || kinds[KindRoot] == 0 {
		t.Errorf("kinds = %v, want some of each", kinds)
	}
	if k := Kind(project.CompilationError{Message: "Model 'a' depends on a node named 'b' which was not found"}); k != KindMissing {
		t.Errorf("dbt's missing node is %q, want %q", k, KindMissing)
	}
}

// fakeDryRun answers dry runs from rules keyed by a substring of the query.
type fakeDryRun struct {
	mu      sync.Mutex
	queries []string
	rules   []rule
}

type rule struct {
	when string // the query contains this
	// token, when set, makes the error's [line:col] point at it in the query.
	token string
	msg   string
	err   error
	bytes int64
}

func (f *fakeDryRun) run(_ context.Context, _ *project.Action, q string) (*bigquery.DryRun, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	for _, r := range f.rules {
		if !strings.Contains(q, r.when) {
			continue
		}
		if r.err != nil {
			return nil, r.err
		}
		if r.msg == "" {
			return &bigquery.DryRun{Bytes: r.bytes, Accuracy: bigquery.AccuracyPrecise}, nil
		}
		msg := r.msg
		if r.token != "" {
			i := strings.Index(q, r.token)
			line := strings.Count(q[:i], "\n") + 1
			col := i - strings.LastIndex(q[:i], "\n")
			msg += fmt.Sprintf(" at [%d:%d]", line, col)
		}
		return nil, &bigquery.APIError{Status: 400, Message: msg}
	}
	return &bigquery.DryRun{Bytes: 1 << 30, Accuracy: bigquery.AccuracyPrecise}, nil
}

func TestDryRunIncrementalWithOps(t *testing.T) {
	p := testProject(t, "incremental")
	file := filepath.Join(p.Root, "definitions", "daily_events.sqlx")
	src := read(t, file)
	f := &fakeDryRun{rules: []rule{
		// Only the incremental query has this; it comes from a substitution.
		{when: "WHERE DATE(event_ts) > checkpoint", token: "checkpoint\nGROUP", msg: "Unrecognized name: checkpoint; Did you mean checkpoints?"},
		{when: "ALTER TABLE", token: "ALTER", msg: "Syntax error: Unexpected keyword ALTER"},
	}}
	got := renderAll(src, DryRunFile(context.Background(), p, file, src, f.run))
	incLine := lineOf(t, src, "${when(incremental(), `WHERE")
	alterLine := lineOf(t, src, "ALTER TABLE")
	want := []string{
		fmt.Sprintf("%d error %q: Syntax error: Unexpected keyword ALTER", alterLine, "ALTER"),
		fmt.Sprintf("%d error %q: Unrecognized name: checkpoint; Did you mean checkpoints? (incremental query) [fix checkpoints]", incLine, "${when(incremental(), `WHERE DATE(event_ts) > checkpoint`)}"),
		"0 hint \"config {\": Dry run: full refresh 1.00 GB processed (≈ $0.0061)",
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// pre_operations went ahead of both queries and of post_operations.
	for _, q := range f.queries {
		if !strings.Contains(q, "DECLARE checkpoint") {
			t.Errorf("dry run without pre_operations:\n%s", q)
		}
	}
}

func TestDryRunPlacesErrors(t *testing.T) {
	tests := []struct {
		name    string
		project string
		file    string
		rule    rule
		// want is the source text the error should mark, and on which line.
		wantText, wantLine string
	}{
		{"literal token in the main query", "incremental", "definitions/daily_events.sqlx",
			rule{when: "SELECT DATE(event_ts)", token: "event_name, COUNT", msg: "Unrecognized name: event_name"}, "event_name", "SELECT DATE(event_ts) AS event_date, event_name"},
		{"pre_operations after a multi-line substitution", "incremental", "definitions/daily_events.sqlx",
			rule{when: "DECLARE checkpoint", token: "  );", msg: "Syntax error: Unexpected )"}, ");", "  );"},
		{"second statement of an operation", "basic", "definitions/refresh_permissions.sqlx",
			rule{when: "SELECT 1", token: "SELECT 1", msg: "Syntax error: bad SELECT"}, "SELECT", "SELECT 1"},
		{"generated assertion goes on the assertions key", "basic", "definitions/daily_orders.sqlx",
			rule{when: "index_row_count", token: "index_row_count >", msg: "Unrecognized name: index_row_count"}, "assertions", "  assertions: {"},
		{"action defined in JavaScript", "navigation", "definitions/sources.js",
			rule{when: "raw-project.raw.events", token: "raw-project", msg: "Syntax error: nope"}, "js_view", `publish("js_view"`},
		{"not found without a position lands on the ref", "navigation", "definitions/marts/report.sqlx",
			rule{when: "dev_js_view", msg: "Not found: Table demo-project:analytics.dev_js_view was not found in location EU"}, `${ref("js_view")}`, `JOIN ${ref("js_view")}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testProject(t, tt.project)
			file := filepath.Join(p.Root, tt.file)
			src := read(t, file)
			f := &fakeDryRun{rules: []rule{tt.rule}}
			var errs []Diagnostic
			for _, d := range DryRunFile(context.Background(), p, file, src, f.run) {
				if d.Severity == Error {
					errs = append(errs, d)
				}
			}
			if len(errs) != 1 {
				t.Fatalf("got %v", renderAll(src, errs))
			}
			d := errs[0]
			if d.Line != lineOf(t, src, tt.wantLine) {
				t.Errorf("line %d, want %d: %s", d.Line, lineOf(t, src, tt.wantLine), render(src, d))
			}
			if got := string(lineText(src, d.Line)[d.Start:d.End]); got != tt.wantText {
				t.Errorf("marks %q, want %q", got, tt.wantText)
			}
		})
	}
}

func TestDryRunFailures(t *testing.T) {
	p := testProject(t, "basic")
	file := filepath.Join(p.Root, "definitions", "weekly_revenue.sqlx")
	src := read(t, file)

	f := &fakeDryRun{rules: []rule{{when: "SELECT", err: fmt.Errorf("%w: invalid_rapt", bigquery.ErrAuth)}}}
	got := renderAll(src, DryRunFile(context.Background(), p, file, src, f.run))
	want := []string{"0 hint \"config { type: \\\"view\\\" }\": Dry run didn't run: no valid Google credentials: invalid_rapt; run `gcloud auth application-default login`"}
	if !slices.Equal(got, want) {
		t.Errorf("auth: got %q", got)
	}

	f = &fakeDryRun{rules: []rule{{when: "SELECT", err: &bigquery.APIError{Status: 403, Message: "Access Denied: Project p: User does not have bigquery.jobs.create permission"}}}}
	if got := renderAll(src, DryRunFile(context.Background(), p, file, src, f.run)); len(got) != 1 || !strings.Contains(got[0], "hint") || !strings.Contains(got[0], "jobs.create") {
		t.Errorf("403: got %q", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := DryRunFile(ctx, p, file, src, (&fakeDryRun{}).run); got != nil {
		t.Errorf("cancelled: got %v", got)
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	slow := func(ctx context.Context, _ *project.Action, _ string) (*bigquery.DryRun, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if got := renderAll(src, DryRunFile(ctx, p, file, src, slow)); len(got) != 1 || !strings.HasSuffix(got[0], "Dry run didn't run: timed out") {
		t.Errorf("timed out: got %q", got)
	}

	unknown := func(context.Context, *project.Action, string) (*bigquery.DryRun, error) {
		return &bigquery.DryRun{Accuracy: bigquery.AccuracyUnknown}, nil
	}
	if got := renderAll(src, DryRunFile(context.Background(), p, file, src, unknown)); len(got) != 1 || !strings.HasSuffix(got[0], "Dry run: bytes unknown") {
		t.Errorf("unknown bytes: got %q", got)
	}
}

func TestCostText(t *testing.T) {
	tests := []struct {
		label string
		bytes int64
		want  string
	}{
		{"", 512, "512 B processed (≈ $0.0000)"},
		{"", 1536, "1.50 KB processed (≈ $0.0000)"},
		{"full", 3 << 40, "full refresh 3.00 TB processed (≈ $18.75)"},
		{"incremental", 20 << 20, "incremental 20.00 MB processed (≈ $0.0001)"},
	}
	for _, tt := range tests {
		if got := costText(tt.label, &bigquery.DryRun{Bytes: tt.bytes, Accuracy: bigquery.AccuracyPrecise}); got != tt.want {
			t.Errorf("costText(%q, %d) = %q, want %q", tt.label, tt.bytes, got, tt.want)
		}
	}
	// Seen from BigQuery: a real figure with UNKNOWN accuracy.
	if got := costText("", &bigquery.DryRun{Bytes: 1212666725, Accuracy: bigquery.AccuracyUnknown}); got != "1.13 GB processed (≈ $0.0069)" {
		t.Errorf("unknown accuracy with bytes: %q", got)
	}
	if got := costText("", &bigquery.DryRun{Accuracy: bigquery.AccuracyUnknown}); got != "bytes unknown" {
		t.Errorf("unknown accuracy without bytes: %q", got)
	}
}

// TestDryRunSendsActionSQL checks that diagnostics dry-run exactly the SQL
// the TUI does for each action, so one cached dry run serves both.
func TestDryRunSendsActionSQL(t *testing.T) {
	checked := 0
	for _, name := range []string{"basic", "incremental", "navigation"} {
		p := testProject(t, name)
		for _, a := range p.Graph.Actions {
			if a.FileName == "" {
				continue
			}
			file := filepath.Join(p.Root, filepath.FromSlash(a.FileName))
			src, err := os.ReadFile(file)
			if err != nil {
				continue // the fixture project leaves some files out
			}
			f := &fakeDryRun{}
			DryRunFile(context.Background(), p, file, src, f.run)
			for _, q := range a.DryRunQueries() {
				if !slices.Contains(f.queries, q.SQL) {
					t.Errorf("%s: %s: diagnostics did not send the TUI's %q query:\n%s", name, a.ID, q.Label, q.SQL)
				}
				checked++
			}
		}
	}
	if checked < 10 {
		t.Errorf("only %d queries checked; the fixtures went missing", checked)
	}
}

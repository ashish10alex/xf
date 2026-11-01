package dbt

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

// twoParsed is a parsed project of models a and b; twoCompiled is it
// compiled.
const twoParsed = `{
  "metadata": {"dbt_version": "2.0.6"},
  "nodes": {
    "model.p.a": {"unique_id": "model.p.a", "resource_type": "model", "name": "a", "package_name": "p", "fqn": ["p", "a"], "database": "d", "schema": "s", "alias": "a",
      "original_file_path": "models/a.sql", "raw_code": "select {{ 1 }}", "config": {"materialized": "view"}},
    "model.p.b": {"unique_id": "model.p.b", "resource_type": "model", "name": "b", "package_name": "p", "fqn": ["p", "b"], "database": "d", "schema": "s", "alias": "b",
      "original_file_path": "models/b.sql", "raw_code": "select {{ 2 }}", "config": {"materialized": "view"}},
    "seed.p.c": {"unique_id": "seed.p.c", "resource_type": "seed", "name": "c", "package_name": "p", "database": "d", "schema": "s", "alias": "c",
      "original_file_path": "seeds/c.csv"}
  }
}`

var twoCompiled = strings.NewReplacer(
	`"raw_code": "select {{ 1 }}"`, `"raw_code": "select {{ 1 }}", "compiled_code": "select 1"`,
	`"raw_code": "select {{ 2 }}"`, `"raw_code": "select {{ 2 }}", "compiled_code": "select 2"`,
).Replace(twoParsed)

func sqlOf(t *testing.T, g *project.Graph, id project.ActionID) (string, project.SQLState) {
	t.Helper()
	a := g.Actions[id]
	if a == nil {
		t.Fatalf("no action %s", id)
	}
	return a.Query, a.SQL
}

func TestStageParsesAndKeepsUnchangedSQL(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": twoParsed, "FAKE_DBT_COMPILE": twoCompiled})
	root := t.TempDir()
	ctx := context.Background()
	g, final, err := b.Stage(ctx, root, nil)
	if err != nil || final {
		t.Fatalf("Stage = final %v, %v", final, err)
	}
	if got := calls(); !slices.Equal(got, []string{"parse"}) {
		t.Errorf("ran %v, want only parse", got)
	}
	for _, id := range []project.ActionID{"d.s.a", "d.s.b"} {
		if q, st := sqlOf(t, g, id); st != project.SQLRaw || !strings.Contains(q, "{{") {
			t.Errorf("%s = %q in state %d, want its template, raw", id, q, st)
		}
	}
	if _, st := sqlOf(t, g, "d.s.c"); st != project.SQLCompiled {
		t.Errorf("a seed has nothing to compile, yet is in state %d", st)
	}
	// The states survive the trip to another process.
	if back, err := b.Parse(g.JSON); err != nil || back.Actions["d.s.a"].SQL != project.SQLRaw {
		t.Errorf("parsed back: %v, %+v", err, back)
	}

	// The compile that follows skips its own parse, and its SQL is whole.
	whole, err := b.Compile(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(); !slices.Equal(got, []string{"parse", "compile"}) {
		t.Errorf("ran %v, want parse then compile", got)
	}
	if q, st := sqlOf(t, whole, "d.s.a"); q != "select 1" || st != project.SQLCompiled {
		t.Errorf("compiled a = %q in state %d", q, st)
	}

	// After an edit to b, a keeps its compiled SQL, marked outdated, and b
	// shows its new template.
	edited := filepath.Join(t.TempDir(), "edited.json")
	if err := os.WriteFile(edited, []byte(strings.Replace(twoParsed, "select {{ 2 }}", "select {{ 3 }}", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DBT_PARSE", edited)
	g, _, err = b.Stage(ctx, root, whole)
	if err != nil {
		t.Fatal(err)
	}
	if q, st := sqlOf(t, g, "d.s.a"); q != "select 1" || st != project.SQLOutdated {
		t.Errorf("a = %q in state %d, want the last compile's SQL, outdated", q, st)
	}
	if q, st := sqlOf(t, g, "d.s.b"); q != "select {{ 3 }}" || st != project.SQLRaw {
		t.Errorf("b = %q in state %d, want its new template, raw", q, st)
	}
}

func TestStageIsFinalWithHooksOrErrors(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": hookManifest, "FAKE_DBT_COMPILE": plainManifest})
	root := t.TempDir()
	g, final, err := b.Stage(context.Background(), root, nil)
	if err != nil || !final || !strings.Contains(g.Notice, "runs on-run-start and on-run-end hooks") {
		t.Fatalf("Stage = final %v, %v, notice %q; want the parsed graph and nothing after it", final, err, g.Notice)
	}
	if a := g.Actions["d.s.a"]; a.SQL != project.SQLCompiled {
		t.Errorf("a final graph's actions wait for no compile, yet a is in state %d", a.SQL)
	}
	if fg, err := b.Focus(context.Background(), root, g, "d.s.a"); fg != nil || err != nil {
		t.Errorf("Focus = %v, %v; a compile would run the hooks", fg, err)
	}
	if got := calls(); !slices.Equal(got, []string{"parse"}) {
		t.Errorf("ran %v, want only parse", got)
	}
}

func TestStageWithHooksOptInGoesOnButNeverFocuses(t *testing.T) {
	b, calls := fakeBackend(t, true, map[string]string{"FAKE_DBT_PARSE": hookManifest, "FAKE_DBT_COMPILE": plainManifest})
	root := t.TempDir()
	g, final, err := b.Stage(context.Background(), root, nil)
	if err != nil || final || g.Notice != "" || g.Actions["d.s.a"].SQL != project.SQLRaw {
		t.Fatalf("Stage = final %v, %v, notice %q", final, err, g.Notice)
	}
	if fg, err := b.Focus(context.Background(), root, g, "d.s.a"); fg != nil || err != nil {
		t.Errorf("Focus = %v, %v; each one would run the hooks", fg, err)
	}
	if got := calls(); !slices.Equal(got, []string{"parse"}) {
		t.Errorf("ran %v, want only parse", got)
	}
}

func TestFocusCompilesOneAction(t *testing.T) {
	// The focused compile's manifest has every node, and SQL for the one
	// selected.
	focused := strings.Replace(twoParsed, `"raw_code": "select {{ 2 }}"`, `"raw_code": "select {{ 2 }}", "compiled_code": "select 2"`, 1)
	b, calls, dir := fakeBackendTarget(t, "", false, map[string]string{"FAKE_DBT_PARSE": twoParsed, "FAKE_DBT_COMPILE": focused, "FAKE_DBT_ARGS": "1"})
	root := t.TempDir()
	ctx := context.Background()
	staged, _, err := b.Stage(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := b.Focus(ctx, root, staged, "d.s.b")
	if err != nil || g == nil {
		t.Fatalf("Focus = %v, %v", g, err)
	}
	if q, st := sqlOf(t, g, "d.s.b"); q != "select 2" || st != project.SQLCompiled {
		t.Errorf("b = %q in state %d, want its compiled SQL", q, st)
	}
	if q, st := sqlOf(t, g, "d.s.a"); q != "select {{ 1 }}" || st != project.SQLRaw {
		t.Errorf("a = %q in state %d, want it untouched", q, st)
	}
	if _, st := sqlOf(t, staged, "d.s.b"); st != project.SQLRaw {
		t.Error("Focus changed the graph it was given")
	}
	if got := calls(); !slices.Equal(got, []string{"parse", "compile"}) {
		t.Errorf("ran %v", got)
	}
	// It ran in a directory of its own, selecting just that node.
	focusTarget := filepath.Join(dir, b.Variant(), focusDir, "target")
	if _, err := os.Stat(filepath.Join(focusTarget, "manifest.json")); err != nil {
		t.Errorf("no manifest in the focus directory: %v", err)
	}
	args, _ := os.ReadFile(filepath.Join(focusTarget, "args"))
	if !strings.Contains(string(args), "--select p.b") {
		t.Errorf("args = %q, want --select p.b", args)
	}

	// Nothing to do for an action that is compiled, or isn't there.
	for _, id := range []project.ActionID{"d.s.b", "d.s.c", "d.s.nope"} {
		if again, err := b.Focus(ctx, root, g, id); again != nil || err != nil {
			t.Errorf("Focus(%s) = %v, %v, want nothing", id, again, err)
		}
	}
}

// A model's tests are compiled with it, in the same dbt run; one that comes
// back without SQL waits for the project's compile.
func TestFocusCompilesAModelWithItsTests(t *testing.T) {
	const tests = `,
    "test.p.not_null_b": {"unique_id": "test.p.not_null_b", "resource_type": "test", "name": "not_null_b", "package_name": "p", "fqn": ["p", "not_null_b"],
      "database": "d", "schema": "audit", "alias": "not_null_b", "original_file_path": "models/schema.yml", "raw_code": "{{ test_not_null() }}",
      "attached_node": "model.p.b", "depends_on": {"nodes": ["model.p.b"]}, "config": {}},
    "test.p.unique_b": {"unique_id": "test.p.unique_b", "resource_type": "test", "name": "unique_b", "package_name": "p", "fqn": ["p", "unique_b"],
      "database": "d", "schema": "audit", "alias": "unique_b", "original_file_path": "models/schema.yml", "raw_code": "{{ test_unique() }}",
      "attached_node": "model.p.b", "depends_on": {"nodes": ["model.p.b"]}, "config": {}},
    "seed.p.c":`
	parsed := strings.Replace(twoParsed, `,
    "seed.p.c":`, tests, 1)
	if parsed == twoParsed {
		t.Fatal("the tests weren't added to the manifest")
	}
	// dbt compiles b and one of its two tests.
	focused := strings.NewReplacer(
		`"raw_code": "select {{ 2 }}"`, `"raw_code": "select {{ 2 }}", "compiled_code": "select 2"`,
		`"raw_code": "{{ test_not_null() }}"`, `"raw_code": "{{ test_not_null() }}", "compiled_code": "select b where b is null"`,
	).Replace(parsed)
	b, _, dir := fakeBackendTarget(t, "", false, map[string]string{"FAKE_DBT_PARSE": parsed, "FAKE_DBT_COMPILE": focused, "FAKE_DBT_ARGS": "1"})
	root := t.TempDir()
	ctx := context.Background()
	staged, _, err := b.Stage(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, st := sqlOf(t, staged, "d.audit.not_null_b"); st != project.SQLRaw {
		t.Fatalf("a parsed test is in state %d, want raw", st)
	}
	// Asked for by the test, the pane is the model's all the same.
	g, err := b.Focus(ctx, root, staged, "d.audit.not_null_b")
	if err != nil || g == nil {
		t.Fatalf("Focus = %v, %v", g, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, b.Variant(), focusDir, "target", "args"))
	if !strings.Contains(string(args), "--select p.not_null_b p.b p.unique_b") {
		t.Errorf("args = %q, want the test, its model and the model's other test selected", args)
	}
	for id, want := range map[project.ActionID]project.SQLState{
		"d.s.b": project.SQLCompiled, "d.audit.not_null_b": project.SQLCompiled,
		"d.audit.unique_b": project.SQLRaw, "d.s.a": project.SQLRaw,
	} {
		if _, st := sqlOf(t, g, id); st != want {
			t.Errorf("%s is in state %d, want %d", id, st, want)
		}
	}
	if q, _ := sqlOf(t, g, "d.audit.not_null_b"); q != "select b where b is null" {
		t.Errorf("the test's SQL = %q", q)
	}
}

func TestFocusReportsErrors(t *testing.T) {
	b, _ := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": twoParsed, "FAKE_DBT_COMPILE": twoParsed, "FAKE_DBT_COMPILE_EXIT": "1",
		"FAKE_DBT_LOG": `{"info": {"level": "error", "msg": "bad jinja in models/a.sql", "code": "dbt1501"}}`})
	root := t.TempDir()
	staged, _, err := b.Stage(context.Background(), root, nil)
	if err != nil || staged.HasErrors() {
		t.Skipf("the fake's log makes the parse fail too: %v", err)
	}
	g, err := b.Focus(context.Background(), root, staged, "d.s.a")
	if err != nil || g == nil || !g.HasErrors() {
		t.Fatalf("Focus = %+v, %v; want the action's errors", g, err)
	}
}

func TestSuggestPicksTheNewestUncompiledFile(t *testing.T) {
	b, _ := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": twoParsed})
	root := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{"a.sql": time.Hour, "b.sql": time.Minute} {
		p := filepath.Join(root, "models", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("select 1"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	g, _, err := b.Stage(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Suggest(context.Background(), root, g); got != "d.s.b" {
		t.Errorf("Suggest = %q, want d.s.b, written last", got)
	}
}

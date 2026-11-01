package dbt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/testutil"
)

func testdata(t *testing.T, parts ...string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "..", "testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func loadGraph(t *testing.T, fixture string) *project.Graph {
	t.Helper()
	data, err := os.ReadFile(testdata(t, "examples", fixture))
	if err != nil {
		t.Fatal(err)
	}
	g, err := (&Backend{}).Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// The example compiled by dbt v2 and by dbt-core gives the same graph, on
// macOS and on Windows (where dbt-core writes backslash paths).
var fixtures = []string{"dbt-v2.json", "dbt-core.json", "dbt-v2-windows.json", "dbt-core-windows.json"}

func TestGraphKinds(t *testing.T) {
	want := map[project.ActionKind]int{
		project.KindTable:            4, // dim_customers, customer_segments v1 and v2, revenue_report
		project.KindView:             3,
		project.KindIncremental:      1,
		project.KindMaterializedView: 1,
		project.KindEphemeral:        1,
		project.KindSeed:             1,
		project.KindSnapshot:         1,
		project.KindTest:             13, // 12 data tests and a unit test
		project.KindAnalysis:         1,
		project.KindSource:           3,
		project.KindExposure:         1,
	}
	for _, f := range fixtures {
		t.Run(f, func(t *testing.T) {
			g := loadGraph(t, f)
			got := map[project.ActionKind]int{}
			for _, grp := range g.Grouped() {
				got[grp.Kind] = len(grp.Actions)
			}
			for k, n := range want {
				if got[k] != n {
					t.Errorf("%d %s actions, want %d", got[k], k, n)
				}
			}
			for k, n := range got {
				if _, ok := want[k]; !ok {
					t.Errorf("unexpected %d %s actions", n, k)
				}
			}
			if g.HasErrors() || g.Notice != "" {
				t.Errorf("errors %v, notice %q", g.Errors, g.Notice)
			}
		})
	}
}

func TestGraphActions(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f, func(t *testing.T) {
			g := loadGraph(t, f)
			get := func(id project.ActionID) *project.Action {
				t.Helper()
				a := g.Actions[id]
				if a == nil {
					t.Fatalf("no action %s", id)
				}
				return a
			}
			fct := get("alex-personal-dev-01.xf_example.fct_orders")
			if fct.FileName != "models/marts/fct_orders.sql" || fct.PartitionBy != "order_date" || !slices.Contains(fct.Tags, "hourly") {
				t.Errorf("fct_orders = %+v", fct)
			}
			if !strings.Contains(fct.Query, "from `alex-personal-dev-01`.`xf_example`.`stg_orders` as o") || strings.Contains(fct.Query, "is_incremental") {
				t.Errorf("fct_orders query isn't compiled:\n%s", fct.Query)
			}
			if fct.HasIncrementalVariant() {
				t.Error("a dbt incremental model has one compiled variant")
			}
			deps := ids(g.Dependencies(fct.ID))
			if want := []string{"alex-personal-dev-01.xf_example.stg_orders", "alex-personal-dev-01.xf_example.stg_payments"}; !slices.Equal(deps, want) {
				t.Errorf("fct_orders reads %v, want %v", deps, want)
			}

			dim := get("alex-personal-dev-01.xf_example.dim_customers")
			if len(dim.PreOps) != 1 || len(dim.PostOps) != 1 || !strings.Contains(dim.PostOps[0], "{{ this }}") {
				t.Errorf("dim_customers hooks = %q, %q", dim.PreOps, dim.PostOps)
			}
			if len(dim.Columns) != 3 || dim.Columns[0].Path[0] != "customer_id" || dim.Columns[2].Path[0] != "lifetime_value" {
				t.Errorf("dim_customers columns, in YAML order = %+v", dim.Columns)
			}

			src := get("alex-personal-dev-01.raw.customers")
			if src.Kind != project.KindSource || !src.Kind.External() || src.Runnable() {
				t.Errorf("raw.customers = %+v", src)
			}
			stg := get("alex-personal-dev-01.xf_example.stg_customers")
			if got := ids(g.Dependencies(stg.ID)); !slices.Equal(got, []string{"alex-personal-dev-01.raw.customers"}) {
				t.Errorf("stg_customers reads %v", got)
			}

			test := get("alex-personal-dev-01.xf_example_dbt_test__audit.unique_dim_customers_customer_id")
			if test.Kind != project.KindTest || test.ParentAction == nil || test.ParentAction.ID() != dim.ID {
				t.Errorf("unique test = %+v", test)
			}
			unit := get("unit_test.test_fct_orders_sums_payments")
			if unit.ParentAction == nil || unit.ParentAction.ID() != fct.ID || unit.Kind.HasTable() {
				t.Errorf("unit test = %+v", unit)
			}

			exp := get("exposure.revenue_dashboard")
			if exp.Kind != project.KindExposure || exp.Description != "The finance team's revenue dashboard." {
				t.Errorf("exposure = %+v", exp)
			}
			if got := ids(g.Dependencies(exp.ID)); !slices.Equal(got, []string{"alex-personal-dev-01.xf_example.daily_revenue", "alex-personal-dev-01.xf_example_reporting.revenue_report"}) {
				t.Errorf("exposure reads %v", got)
			}

			eph := get("alex-personal-dev-01.xf_example.int_customer_countries")
			if eph.Kind != project.KindEphemeral || eph.Kind.HasTable() {
				t.Errorf("ephemeral = %+v", eph)
			}
			v2 := get("alex-personal-dev-01.xf_example.customer_segments_v2")
			if !strings.Contains(v2.Query, "__dbt__cte__int_customer_countries") {
				t.Errorf("customer_segments v2 doesn't inline the ephemeral model:\n%s", v2.Query)
			}
			seed := get("alex-personal-dev-01.xf_example_reference.country_codes")
			if seed.Kind != project.KindSeed || len(seed.SQLSections(false)) != 0 {
				t.Errorf("seed = %+v", seed)
			}
			get("alex-personal-dev-01.xf_example_snapshots.customers_snapshot")
			get("alex-personal-dev-01.xf_example.revenue_by_country")
			if a := get("alex-personal-dev-01.xf_example.daily_revenue"); a.Kind != project.KindMaterializedView {
				t.Errorf("daily_revenue = %s", a.Kind)
			}

			// What dbt build --select takes: the fqn, which dbt-core and v2
			// match to just that node. Nodes dbt build doesn't build have none.
			for id, want := range map[project.ActionID]string{
				fct.ID:  "xf_example.marts.fct_orders",
				v2.ID:   "xf_example.marts.customer_segments.v2",
				seed.ID: "xf_example.country_codes",
				test.ID: "xf_example.marts.unique_dim_customers_customer_id",
				unit.ID: "xf_example.marts.fct_orders.test_fct_orders_sums_payments",
				"alex-personal-dev-01.xf_example_snapshots.customers_snapshot": "xf_example.customers_snapshot",
				src.ID: "", eph.ID: "", exp.ID: "", "alex-personal-dev-01.xf_example.revenue_by_country": "",
			} {
				if a := get(id); a.RunName != want || a.Runnable() != (want != "") {
					t.Errorf("%s: RunName %q, runnable %v; want %q", id, a.RunName, a.Runnable(), want)
				}
			}
		})
	}
}

func TestParseRoundTrip(t *testing.T) {
	g := loadGraph(t, "dbt-v2.json")
	again, err := (&Backend{}).Parse(g.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Actions) != len(g.Actions) || string(again.JSON) != string(g.JSON) {
		t.Errorf("round trip: %d actions, want %d; JSON equal %v", len(again.Actions), len(g.Actions), string(again.JSON) == string(g.JSON))
	}
	a, b := g.Actions["alex-personal-dev-01.xf_example.dim_customers"], again.Actions["alex-personal-dev-01.xf_example.dim_customers"]
	if a.Query != b.Query || len(a.Columns) != len(b.Columns) || a.Columns[0].Path[0] != b.Columns[0].Path[0] {
		t.Errorf("dim_customers changed in the round trip")
	}
}

func TestBrokenFixtures(t *testing.T) {
	for _, f := range []string{"dbt-v2-broken.json", "dbt-core-broken.json", "dbt-v2-broken-windows.json", "dbt-core-broken-windows.json"} {
		t.Run(f, func(t *testing.T) {
			g := loadGraph(t, f)
			if len(g.Errors) != 1 {
				t.Fatalf("errors = %+v", g.Errors)
			}
			if e := g.Errors[0]; e.FileName != "models/order_totals.sql" || !strings.Contains(e.Message, "order_lines") {
				t.Errorf("error = %+v", e)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		out     string
		major   int
		version string
	}{
		{"dbt 2.0.6\n", 2, "dbt 2.0.6"},
		{"Core:\n  - installed: 1.12.5\n  - latest:    1.12.5 - \x1b[32mUp to date!\x1b[0m\n\nPlugins:\n  - bigquery: 1.12.1\n", 1, "dbt-core 1.12.5"},
		{"Core:\n  - installed: 1.10.0-b2\n", 1, "dbt-core 1.10.0-b2"},
		{"something else\n", 0, ""},
	}
	for _, tt := range tests {
		major, version := parseVersion(tt.out)
		if major != tt.major || version != tt.version {
			t.Errorf("parseVersion(%q) = %d, %q; want %d, %q", tt.out, major, version, tt.major, tt.version)
		}
	}
}

func TestLogErrors(t *testing.T) {
	out := strings.Join([]string{
		`{"data":{},"info":{"level":"info","msg":"Running with dbt=2.0.6","name":"MainReportVersion"}}`,
		`not json at all`,
		// dbt v2: the location is the last line of the message.
		`{"data":{},"info":{"level":"error","msg":"[error] [DependencyNotFound (dbt1048)]: Ref 'order_lines' not found in project.\n  --> models/order_totals.sql:4:6","name":"DependencyNotFound"}}`,
		`{"data":{},"info":{"level":"error","msg":"[error] [DependencyNotFound (dbt1048)]: Ref 'order_lines' not found in project.\n  --> models/order_totals.sql:4:6","name":"DependencyNotFound"}}`,
		// dbt-core: the exception in data.exc, the file in parentheses.
		`{"data":{"exc":"Compilation Error\n  Model 'model.p.a' (models/a.sql) depends on a node named 'b' which was not found"},"info":{"level":"error","msg":"[\u001b[31mERROR\u001b[0m]: Encountered an error:\nCompilation Error\n  Model 'model.p.a' (models/a.sql) depends on a node named 'b' which was not found","name":"MainEncounteredError"}}`,
		// dbt-core on Windows names the file with backslashes.
		`{"data":{"exc":"Compilation Error\n  Model 'model.p.c' (models\\c.sql) depends on a node named 'd' which was not found"},"info":{"level":"error","msg":"x","name":"MainEncounteredError"}}`,
		// No file at all.
		`{"data":{},"info":{"level":"error","msg":"[error] Could not find profile named 'nope'","name":"Generic"}}`,
		`{"data":{},"info":{"level":"warn","msg":"[warn] something","name":"Generic"}}`,
	}, "\n")
	got := logErrors([]byte(out))
	want := []project.CompilationError{
		{FileName: "models/order_totals.sql", Message: "[DependencyNotFound (dbt1048)]: Ref 'order_lines' not found in project.", Stack: "models/order_totals.sql:4"},
		{FileName: "models/a.sql", Message: "Compilation Error\n  Model 'model.p.a' (models/a.sql) depends on a node named 'b' which was not found"},
		{FileName: "models/c.sql", Message: "Compilation Error\n  Model 'model.p.c' (models\\c.sql) depends on a node named 'd' which was not found"},
		{Message: "Could not find profile named 'nope'"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("logErrors =\n%q\nwant\n%q", got, want)
	}
}

const hookManifest = `{
  "metadata": {"dbt_version": "2.0.6"},
  "nodes": {
    "model.p.a": {"unique_id": "model.p.a", "resource_type": "model", "name": "a", "database": "d", "schema": "s", "alias": "a",
      "original_file_path": "models/a.sql", "raw_code": "select {{ 1 }}", "config": {"materialized": "view"}},
    "operation.p.p-on-run-start-0": {"unique_id": "operation.p.p-on-run-start-0", "resource_type": "operation", "name": "p-on-run-start-0",
      "database": "d", "schema": "s", "alias": "p-on-run-start-0", "original_file_path": "./dbt_project.yml", "raw_code": "grant all"}
  }
}`

const plainManifest = `{
  "metadata": {"dbt_version": "2.0.6"},
  "nodes": {
    "model.p.a": {"unique_id": "model.p.a", "resource_type": "model", "name": "a", "database": "d", "schema": "s", "alias": "a",
      "original_file_path": "models/a.sql", "raw_code": "select {{ 1 }}", "compiled_code": "select 1", "config": {"materialized": "view"}}
  }
}`

// fakeBackend runs the fake dbt CLI with env, recording the commands
// it runs.
func fakeBackend(t *testing.T, withHooks bool, env map[string]string) (*Backend, func() []string) {
	t.Helper()
	b, calls, _ := fakeBackendTarget(t, "", withHooks, env)
	return b, calls
}

// fakeBackendTarget is fakeBackend compiling with target, and also returns
// the backend's artifact directory.
func fakeBackendTarget(t *testing.T, target string, withHooks bool, env map[string]string) (*Backend, func() []string, string) {
	t.Helper()
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_DBT_CALLS", calls)
	for k, v := range env {
		if (k == "FAKE_DBT_PARSE" || k == "FAKE_DBT_COMPILE") && strings.HasPrefix(v, "{") {
			p := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
			v = p
		}
		t.Setenv(k, v)
	}
	dir := t.TempDir()
	b := New(testutil.FakeCLI(t, "dbt"), target, func(string) string { return dir }, withHooks)
	return b, func() []string {
		data, _ := os.ReadFile(calls)
		return strings.Fields(string(data))
	}, dir
}

func TestCompilePassesTarget(t *testing.T) {
	b, calls, dir := fakeBackendTarget(t, "prod", false, map[string]string{"FAKE_DBT_PARSE": plainManifest, "FAKE_DBT_COMPILE": plainManifest})
	if _, err := b.Compile(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !slices.Equal(got, []string{"parse:prod", "compile:prod"}) {
		t.Errorf("ran %v, want parse and compile with --target prod", got)
	}
	if _, err := os.Stat(filepath.Join(dir, b.Variant(), "target", "manifest.json")); err != nil {
		t.Errorf("manifest not in the variant's directory: %v", err)
	}
}

// Every setting that changes what a compile produces changes the variant,
// so its artifacts and engine are its own.
func TestVariant(t *testing.T) {
	variant := func(target string, hooks bool) string {
		return New("dbt", target, nil, hooks).Variant()
	}
	seen := map[string]string{}
	for _, c := range []struct {
		target string
		hooks  bool
	}{{"", false}, {"", true}, {"dev", false}, {"prod", false}, {"prod", true}} {
		v := variant(c.target, c.hooks)
		if len(v) != 8 || v != variant(c.target, c.hooks) {
			t.Errorf("variant(%q, %t) = %q, want 8 stable hex characters", c.target, c.hooks, v)
		}
		key := fmt.Sprintf("%q/%t", c.target, c.hooks)
		if prev, ok := seen[v]; ok {
			t.Errorf("%s and %s share variant %s", prev, key, v)
		}
		seen[v] = key
	}
}

// Clients using different dbt binaries never share an engine: a client
// with --dbt-bin set to dbt-core must not attach to a serve compiling
// with dbt v2 on PATH.
func TestVariantKeyedOnBinary(t *testing.T) {
	dir := t.TempDir()
	core, v2 := filepath.Join(dir, "dbt-core"), filepath.Join(dir, "dbt")
	for _, p := range []string{core, v2} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	variant := func(bin string) string { return New(bin, "dev", nil, false).Variant() }
	if variant(core) == variant(v2) {
		t.Errorf("%s and %s share variant %s", core, v2, variant(core))
	}
	if variant(core) != variant(core) {
		t.Errorf("variant(%s) is not stable", core)
	}
}

// A symlink is keyed on what it points at: the same binary reached two
// ways shares an engine, and repointing the link (a Homebrew upgrade)
// changes it.
func TestVariantResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	old, upgraded := filepath.Join(dir, "2.0.5", "dbt"), filepath.Join(dir, "2.0.6", "dbt")
	for _, p := range []string{old, upgraded} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "dbt")
	testutil.Symlink(t, old, link)
	variant := func(bin string) string { return New(bin, "", nil, false).Variant() }
	before := variant(link)
	if before != variant(old) {
		t.Errorf("variant(%s) = %s, want variant of its target %s = %s", link, before, old, variant(old))
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	testutil.Symlink(t, upgraded, link)
	if variant(link) == before {
		t.Errorf("variant(%s) unchanged after repointing it from %s to %s", link, old, upgraded)
	}
}

func TestCompileV2WithHooksParsesOnly(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": hookManifest, "FAKE_DBT_COMPILE": plainManifest})
	g, err := b.Compile(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(); !slices.Equal(got, []string{"parse"}) {
		t.Errorf("ran %v, want only parse", got)
	}
	if !strings.Contains(g.Notice, "dbt 2.0.6 runs on-run-start and on-run-end hooks") || !strings.Contains(g.Notice, "has 1 (in dbt_project.yml)") {
		t.Errorf("notice = %q", g.Notice)
	}
	if a := g.Actions["d.s.a"]; a == nil || a.Query != "select {{ 1 }}" {
		t.Errorf("a = %+v, want its raw SQL", a)
	}
	hook := g.Actions["d.s.p-on-run-start-0"]
	if hook == nil || hook.Kind != project.KindOperation || hook.FileName != "dbt_project.yml" || len(hook.Queries) != 1 || hook.Runnable() {
		t.Errorf("hook = %+v, want an operation dbt can't run on its own", hook)
	}
}

func TestDisabledNodesDontRun(t *testing.T) {
	g, err := (&Backend{}).Parse([]byte(`{"nodes": {}, "disabled": {"model.p.old": [{"unique_id": "model.p.old", "resource_type": "model", "name": "old",
		"database": "d", "schema": "s", "original_file_path": "models/old.sql", "fqn": ["p", "old"], "config": {"materialized": "table"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if a := g.Actions["d.s.old"]; a == nil || !a.Disabled || a.Runnable() {
		t.Errorf("old = %+v, want disabled and not runnable", a)
	}
}

func TestCompileV2WithoutHooksCompiles(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": plainManifest, "FAKE_DBT_COMPILE": plainManifest})
	g, err := b.Compile(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(); !slices.Equal(got, []string{"parse", "compile"}) {
		t.Errorf("ran %v, want parse then compile", got)
	}
	if a := g.Actions["d.s.a"]; a == nil || a.Query != "select 1" || g.Notice != "" {
		t.Errorf("a = %+v, notice %q", a, g.Notice)
	}
}

func TestCompileSkipsParseWhileHooklessFilesUnchanged(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": plainManifest, "FAKE_DBT_COMPILE": plainManifest})
	root := t.TempDir()
	project := filepath.Join(root, ProjectFile)
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	compile := func() {
		t.Helper()
		if _, err := b.Compile(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
	write(project, "name: p\n")
	compile()
	compile()
	if got := calls(); !slices.Equal(got, []string{"parse", "compile", "compile"}) {
		t.Fatalf("ran %v, want the second compile to skip the parse", got)
	}
	write(project, "name: p\non-run-start: ['select 1']\n")
	compile()
	write(filepath.Join(root, "dbt_packages", "utils", ProjectFile), "name: utils\n")
	compile()
	if got := calls(); !slices.Equal(got, []string{"parse", "compile", "compile", "parse", "compile", "parse", "compile"}) {
		t.Errorf("ran %v, want a parse after each dbt_project.yml change", got)
	}
}

// A project that has hooks is parsed on every compile: its parsed graph is
// what xf shows.
func TestCompileWithHooksParsesEachTime(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_PARSE": hookManifest})
	root := t.TempDir()
	for range 2 {
		if _, err := b.Compile(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls(); !slices.Equal(got, []string{"parse", "parse"}) {
		t.Errorf("ran %v, want two parses", got)
	}
}

func TestCompileWithHooksOptIn(t *testing.T) {
	b, calls := fakeBackend(t, true, map[string]string{"FAKE_DBT_COMPILE": hookManifest})
	if _, err := b.Compile(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !slices.Equal(got, []string{"compile"}) {
		t.Errorf("ran %v, want only compile", got)
	}
}

func TestCompileCoreCompilesDirectly(t *testing.T) {
	b, calls := fakeBackend(t, false, map[string]string{"FAKE_DBT_VERSION": "Core:\n  - installed: 1.12.5", "FAKE_DBT_COMPILE": hookManifest})
	g, err := b.Compile(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(); !slices.Equal(got, []string{"compile"}) || g.Notice != "" {
		t.Errorf("ran %v with notice %q, want only compile", got, g.Notice)
	}
}

func TestCompileErrors(t *testing.T) {
	coreErr := `{"data":{"exc":"Compilation Error\n  Model 'model.p.a' (models/a.sql) is broken"},"info":{"level":"error","msg":"x"}}`
	tests := []struct {
		name    string
		env     map[string]string
		errHas  string
		wantErr project.CompilationError
		actions int
	}{
		{
			name:    "failed with a manifest",
			env:     map[string]string{"FAKE_DBT_VERSION": "Core:\n  - installed: 1.12.5", "FAKE_DBT_COMPILE": plainManifest, "FAKE_DBT_COMPILE_EXIT": "1", "FAKE_DBT_LOG": coreErr},
			wantErr: project.CompilationError{FileName: "models/a.sql", Message: "Compilation Error\n  Model 'model.p.a' (models/a.sql) is broken"},
			actions: 1,
		},
		{
			name:    "failed without a manifest",
			env:     map[string]string{"FAKE_DBT_VERSION": "Core:\n  - installed: 1.12.5", "FAKE_DBT_COMPILE_EXIT": "2", "FAKE_DBT_LOG": coreErr},
			wantErr: project.CompilationError{FileName: "models/a.sql", Message: "Compilation Error\n  Model 'model.p.a' (models/a.sql) is broken"},
		},
		{
			name:    "failed without logging",
			env:     map[string]string{"FAKE_DBT_PARSE_EXIT": "2", "FAKE_DBT_STDERR": "error: unexpected argument '--log-format'"},
			wantErr: project.CompilationError{Message: "error: unexpected argument '--log-format'"},
		},
		{
			name:   "failed silently",
			env:    map[string]string{"FAKE_DBT_PARSE_EXIT": "3"},
			errHas: "exit status 3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := fakeBackend(t, false, tt.env)
			g, err := b.Compile(context.Background(), t.TempDir())
			if tt.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errHas) {
					t.Fatalf("err = %v, want %q", err, tt.errHas)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Errors) != 1 || g.Errors[0] != tt.wantErr || len(g.Actions) != tt.actions {
				t.Errorf("errors %+v and %d actions; want %+v and %d", g.Errors, len(g.Actions), tt.wantErr, tt.actions)
			}
		})
	}
}

// A compile that writes no manifest must not show the last one.
func TestCompileIgnoresStaleManifest(t *testing.T) {
	b, _ := fakeBackend(t, false, map[string]string{"FAKE_DBT_VERSION": "Core:\n  - installed: 1.12.5", "FAKE_DBT_COMPILE": plainManifest})
	root := t.TempDir()
	if _, err := b.Compile(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DBT_COMPILE", "")
	t.Setenv("FAKE_DBT_COMPILE_EXIT", "1")
	t.Setenv("FAKE_DBT_LOG", `{"data":{},"info":{"level":"error","msg":"[error] boom"}}`)
	g, err := b.Compile(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Actions) != 0 || len(g.Errors) != 1 {
		t.Errorf("got %d actions and errors %+v, want none and the error", len(g.Actions), g.Errors)
	}
}

func ids(actions []*project.Action) []string {
	out := make([]string, len(actions))
	for i, a := range actions {
		out[i] = string(a.ID)
	}
	return out
}

// dbt actions dry-run their compiled query alone: their hooks are Jinja.
// Kinds with no standalone query have nothing to dry-run.
func TestDryRunQueries(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f, func(t *testing.T) {
			g := loadGraph(t, f)
			for name, want := range map[string]int{
				"xf_example.dim_customers":                                    1, // a table with a pre-hook
				"xf_example.fct_orders":                                       1, // incremental, compiled once
				"xf_example.stg_customers":                                    1,
				"xf_example.daily_revenue":                                    1, // materialized view
				"xf_example_snapshots.customers_snapshot":                     1,
				"xf_example_dbt_test__audit.unique_dim_customers_customer_id": 1,
				"xf_example_dbt_test__audit.assert_positive_order_totals":     1,
				"xf_example.int_customer_countries":                           0, // ephemeral
				"xf_example.revenue_by_country":                               0, // analysis
				"xf_example_reference.country_codes":                          0, // seed
				"raw.customers":                                               0, // source
			} {
				a := g.Actions[project.ActionID("alex-personal-dev-01."+name)]
				if a == nil {
					t.Fatalf("no action %s", name)
				}
				qs := a.DryRunQueries()
				if len(qs) != want {
					t.Errorf("%s: %d dry runs, want %d", name, len(qs), want)
					continue
				}
				for _, q := range qs {
					if q.Label != "" || strings.Contains(q.SQL, "{{") || strings.Contains(q.SQL, "declare run_started") {
						t.Errorf("%s dry-runs %q:\n%s", name, q.Label, q.SQL)
					}
				}
			}
			for _, id := range []project.ActionID{"unit_test.test_fct_orders_sums_payments", "exposure.revenue_dashboard"} {
				if qs := g.Actions[id].DryRunQueries(); len(qs) != 0 {
					t.Errorf("%s: %d dry runs, want none", id, len(qs))
				}
			}
		})
	}
	g, err := (&Backend{}).Parse([]byte(hookManifest))
	if err != nil {
		t.Fatal(err)
	}
	if qs := g.Actions["d.s.p-on-run-start-0"].DryRunQueries(); len(qs) != 0 {
		t.Errorf("an on-run hook dry-runs %q", qs)
	}
}

// A model's pane lists it and every test that reads it, as dbt build
// --select would run them; a test is shown with its home model.
func TestSiblings(t *testing.T) {
	g := loadGraph(t, "dbt-v2.json")
	names := func(actions []*project.Action) []string {
		var out []string
		for _, a := range actions {
			out = append(out, a.Target.Name)
		}
		return out
	}
	get := func(name string) *project.Action {
		t.Helper()
		for _, a := range g.Actions {
			if a.Target.Name == name {
				return a
			}
		}
		t.Fatalf("no action %s", name)
		return nil
	}
	stgCustomers := []string{"stg_customers", "not_null_stg_customers_customer_id", "not_null_stg_customers_email",
		"relationships_stg_orders_96411fe0c89b49c3f4da955dfd358ba0", "unique_stg_customers_customer_id"}
	tests := []struct {
		of   string
		want []string
	}{
		{"stg_customers", stgCustomers},
		{"not_null_stg_customers_email", stgCustomers},
		// Attached to stg_orders, though it reads stg_customers too.
		{"relationships_stg_orders_96411fe0c89b49c3f4da955dfd358ba0", []string{"stg_orders",
			"accepted_values_stg_orders_7c9e0be389dac14a09b2140966f0792f", "not_null_stg_orders_customer_id",
			"not_null_stg_orders_order_id", "relationships_stg_orders_96411fe0c89b49c3f4da955dfd358ba0"}},
		// A unit test and a singular test, neither in fct_orders.sql; sorted
		// by schema (unit tests' is "unit_test"), then name.
		{"fct_orders", []string{"fct_orders", "test_fct_orders_sums_payments", "assert_positive_order_totals",
			"not_null_fct_orders_order_id", "unique_fct_orders_order_id"}},
		{"assert_positive_order_totals", []string{"fct_orders", "test_fct_orders_sums_payments", "assert_positive_order_totals",
			"not_null_fct_orders_order_id", "unique_fct_orders_order_id"}},
		{"country_codes", []string{"country_codes"}},
		// One of three sources in _sources.yml, shown alone.
		{"customers", []string{"customers"}},
	}
	for _, tt := range tests {
		if got := names(g.Siblings(get(tt.of))); !slices.Equal(got, tt.want) {
			t.Errorf("siblings of %s = %q\nwant %q", tt.of, got, tt.want)
		}
	}
}

package dataform

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	data, err := os.ReadFile(testdata(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	data, err = ExtractCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	g, err := ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func ids(actions []*project.Action) []string {
	out := make([]string, len(actions))
	for i, a := range actions {
		out[i] = string(a.ID)
	}
	return out
}

func TestExtractCompileJSON(t *testing.T) {
	logger, err := os.ReadFile(testdata(t, "stdout_logger_prefix.txt"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		in       string
		wantKey  string // a substring the extracted JSON must contain
		wantErr  bool
		wantSame bool
	}{
		{name: "single result", in: `{"tables":[],"graphErrors":{}}`, wantSame: true},
		{name: "logger object then result", in: string(logger), wantKey: `"dataformCoreVersion":"2.9.0"`},
		{name: "braces inside strings", in: `{"level":"x","m":"}{"}{"tables":[{"query":"SELECT '}'"}]}`, wantKey: `SELECT '}'`},
		{name: "stray closer before result", in: `} {"projectConfig":{}}`, wantKey: `projectConfig`},
		{name: "last result wins", in: `{"tables":[],"n":1}{"tables":[],"n":2}`, wantKey: `"n":2`},
		{name: "json without compile keys", in: `{"level":"info"}`, wantErr: true},
		{name: "garbage", in: `Error: something broke`, wantErr: true},
		{name: "empty", in: ``, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractCompileJSON([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(got) {
				t.Fatalf("extracted invalid JSON: %s", got)
			}
			if tt.wantSame && string(got) != tt.in {
				t.Errorf("got %s, want input unchanged", got)
			}
			if !strings.Contains(string(got), tt.wantKey) {
				t.Errorf("got %s, want it to contain %s", got, tt.wantKey)
			}
		})
	}
}

func TestParseCompileJSONKinds(t *testing.T) {
	tests := []struct {
		fixture    string
		wantGroups map[project.ActionKind]int
		wantErrors int
		wantCore   string
	}{
		{
			fixture: "basic.json",
			wantGroups: map[project.ActionKind]int{
				project.KindTable: 1, project.KindView: 3, project.KindOperation: 1, project.KindAssertion: 3, project.KindDeclaration: 2,
			},
			wantCore: "3.0.43",
		},
		{
			fixture:    "incremental.json",
			wantGroups: map[project.ActionKind]int{project.KindTable: 1, project.KindIncremental: 2, project.KindDeclaration: 1},
			wantCore:   "3.0.71",
		},
		{
			fixture:    "compile_error.json",
			wantGroups: map[project.ActionKind]int{project.KindTable: 2},
			wantErrors: 4,
			wantCore:   "3.0.71",
		},
		{
			fixture:    "stdout_logger_prefix.txt",
			wantGroups: map[project.ActionKind]int{project.KindView: 1},
			wantCore:   "2.9.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			g := loadGraph(t, tt.fixture)
			got := map[project.ActionKind]int{}
			for _, grp := range g.Grouped() {
				got[grp.Kind] = len(grp.Actions)
			}
			if len(got) != len(tt.wantGroups) {
				t.Errorf("groups = %v, want %v", got, tt.wantGroups)
			}
			for k, n := range tt.wantGroups {
				if got[k] != n {
					t.Errorf("%s: got %d actions, want %d", k, got[k], n)
				}
			}
			if len(g.Errors) != tt.wantErrors {
				t.Errorf("got %d errors, want %d: %v", len(g.Errors), tt.wantErrors, g.Errors)
			}
			if g.CoreVersion != tt.wantCore {
				t.Errorf("core = %q, want %q", g.CoreVersion, tt.wantCore)
			}
		})
	}
}

func TestParseCompileJSONFields(t *testing.T) {
	basic := loadGraph(t, "basic.json")
	inc := loadGraph(t, "incremental.json")
	errs := loadGraph(t, "compile_error.json")

	t.Run("operation queries", func(t *testing.T) {
		op := basic.Actions["demo-project.analytics.refresh_permissions"]
		if op == nil || op.Kind != project.KindOperation {
			t.Fatalf("operation missing or wrong kind: %+v", op)
		}
		if len(op.Queries) != 2 || op.Query != "" {
			t.Errorf("queries = %q, query = %q", op.Queries, op.Query)
		}
	})
	t.Run("auto assertion parent", func(t *testing.T) {
		a := basic.Actions["demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0"]
		if a == nil || a.ParentAction == nil || a.ParentAction.ID() != "demo-project.analytics.daily_orders" {
			t.Fatalf("want parentAction daily_orders, got %+v", a)
		}
		manual := basic.Actions["demo-project.analytics_assertions.revenue_positive"]
		if manual == nil || manual.ParentAction != nil {
			t.Errorf("manual assertion should have no parent: %+v", manual)
		}
	})
	t.Run("incremental fields", func(t *testing.T) {
		a := inc.Actions["demo-project.reporting.daily_events"]
		if a == nil || a.Kind != project.KindIncremental {
			t.Fatalf("got %+v", a)
		}
		if len(a.PreOps) != 1 || len(a.IncrementalPreOps) != 1 || len(a.PostOps) != 1 || len(a.IncrementalPostOps) != 1 {
			t.Errorf("ops: pre=%d incPre=%d post=%d incPost=%d", len(a.PreOps), len(a.IncrementalPreOps), len(a.PostOps), len(a.IncrementalPostOps))
		}
		if !strings.Contains(a.IncrementalQuery, "checkpoint") || strings.Contains(a.Query, "checkpoint") {
			t.Errorf("query/incrementalQuery mixed up")
		}
	})
	t.Run("omitted arrays decode empty", func(t *testing.T) {
		a := inc.Actions["demo-project.reporting.append_only_log"]
		if a.PreOps != nil || a.Tags != nil {
			t.Errorf("want nil preOps/tags, got %q %q", a.PreOps, a.Tags)
		}
	})
	t.Run("declaration without tags", func(t *testing.T) {
		d := basic.Actions["raw-project.shop.orders"]
		if d == nil || d.Kind != project.KindDeclaration || d.Runnable() || d.Tags != nil {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("compilation errors", func(t *testing.T) {
		if !errs.HasErrors() {
			t.Fatal("want errors")
		}
		for _, e := range errs.Errors {
			if e.FileName == "" || e.Message == "" {
				t.Errorf("incomplete error %+v", e)
			}
		}
	})
}

func TestGraphLineage(t *testing.T) {
	g := loadGraph(t, "basic.json")
	tests := []struct {
		id             project.ActionID
		wantDeps       []string
		wantDependents []string
	}{
		{
			id:       "demo-project.analytics.daily_orders",
			wantDeps: []string{"demo-project.staging.stg_customers", "demo-project.staging.stg_orders"},
			wantDependents: []string{
				"demo-project.analytics.refresh_permissions",
				"demo-project.analytics.weekly_revenue",
				"demo-project.analytics_assertions.analytics_daily_orders_assertions_rowConditions",
				"demo-project.analytics_assertions.analytics_daily_orders_assertions_uniqueKey_0",
			},
		},
		{
			id:             "raw-project.shop.orders",
			wantDeps:       []string{},
			wantDependents: []string{"demo-project.staging.stg_orders"},
		},
		{
			id:             "demo-project.analytics_assertions.revenue_positive",
			wantDeps:       []string{"demo-project.analytics.weekly_revenue"},
			wantDependents: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			if got := ids(g.Dependencies(tt.id)); !slices.Equal(got, tt.wantDeps) {
				t.Errorf("deps = %v, want %v", got, tt.wantDeps)
			}
			if got := ids(g.Dependents(tt.id)); !slices.Equal(got, tt.wantDependents) {
				t.Errorf("dependents = %v, want %v", got, tt.wantDependents)
			}
		})
	}
}

func TestUnknownDependencyPlaceholder(t *testing.T) {
	data := []byte(`{"tables":[{"type":"table","target":{"database":"p","schema":"s","name":"t"},
		"dependencyTargets":[{"database":"p","schema":"s","name":"ghost"},{"database":"p","schema":"s","name":"ghost"}]}]}`)
	g, err := ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	ghost := g.Actions["p.s.ghost"]
	if ghost == nil || ghost.Kind != project.KindUnknown || ghost.Runnable() {
		t.Fatalf("want unknown placeholder, got %+v", ghost)
	}
	if got := ids(g.Dependencies("p.s.t")); !slices.Equal(got, []string{"p.s.ghost"}) {
		t.Errorf("deps not de-duplicated: %v", got)
	}
	for _, grp := range g.Grouped() {
		if grp.Kind == project.KindUnknown {
			t.Error("unknown placeholders must not be grouped")
		}
	}
}

func TestSQLSections(t *testing.T) {
	basic := loadGraph(t, "basic.json")
	inc := loadGraph(t, "incremental.json")
	titles := func(s []project.SQLSection) []string {
		out := make([]string, len(s))
		for i, x := range s {
			out[i] = x.Title
		}
		return out
	}
	tests := []struct {
		name        string
		action      *project.Action
		incremental bool
		want        []string
		wantInFirst string
	}{
		{"plain table", basic.Actions["demo-project.analytics.daily_orders"], false, []string{"query"}, "GROUP BY"},
		{"incremental flag ignored for tables", basic.Actions["demo-project.analytics.daily_orders"], true, []string{"query"}, "GROUP BY"},
		{"operation", basic.Actions["demo-project.analytics.refresh_permissions"], false, []string{"operation 1/2", "operation 2/2"}, "GRANT"},
		{"incremental full", inc.Actions["demo-project.reporting.daily_events"], false,
			[]string{"pre_operations", "query", "post_operations"}, "DATE '2000-01-01'"},
		{"incremental incremental", inc.Actions["demo-project.reporting.daily_events"], true,
			[]string{"incremental pre_operations", "incremental query", "incremental post_operations"}, "MAX(event_date)"},
		{"incremental without ops", inc.Actions["demo-project.reporting.append_only_log"], true, []string{"incremental query"}, "MAX(event_ts)"},
		{"declaration has no SQL", basic.Actions["raw-project.shop.orders"], false, []string{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.action.SQLSections(tt.incremental)
			if !slices.Equal(titles(got), tt.want) {
				t.Fatalf("titles = %v, want %v", titles(got), tt.want)
			}
			if len(got) > 0 {
				if !strings.Contains(got[0].SQL, tt.wantInFirst) {
					t.Errorf("first section %q does not contain %q", got[0].SQL, tt.wantInFirst)
				}
				if strings.HasPrefix(got[0].SQL, "\n") {
					t.Errorf("leading newlines not trimmed: %q", got[0].SQL)
				}
			}
		})
	}
}

func TestResolveBinary(t *testing.T) {
	onPath := map[string]string{"dataform": "/usr/local/bin/dataform", "/opt/df": "/opt/df", "/env/df": "/env/df"}
	lookPath := func(name string) (string, error) {
		if p, ok := onPath[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	tests := []struct {
		name    string
		flag    string
		env     string
		path    bool
		want    string
		wantErr string
	}{
		{name: "flag wins", flag: "/opt/df", env: "/env/df", want: "/opt/df"},
		{name: "env next", env: "/env/df", want: "/env/df"},
		{name: "path last", want: "/usr/local/bin/dataform"},
		{name: "bad flag", flag: "/nope/df", wantErr: `at "/nope/df" (from --dataform-bin)`},
		{name: "bad env", env: "/nope/df", wantErr: `(from $DATAFORM_BIN)`},
		{name: "bad env name", env: "nope-df", wantErr: `could not find "nope-df" on PATH (from $DATAFORM_BIN)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string {
				if k == BinEnv {
					return tt.env
				}
				return ""
			}
			got, err := ResolveBinary(tt.flag, getenv, lookPath)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	t.Run("not on PATH", func(t *testing.T) {
		_, err := ResolveBinary("", func(string) string { return "" }, func(string) (string, error) { return "", exec.ErrNotFound })
		if err == nil || !strings.Contains(err.Error(), `could not find "dataform" on PATH`) || !strings.Contains(err.Error(), InstallCommand) {
			t.Errorf("got %v", err)
		}
		if !errors.Is(err, ErrNotOnPath) {
			t.Errorf("%v isn't ErrNotOnPath", err)
		}
	})
	// A binary that was named and isn't there is a setting to fix, not a
	// CLI to install.
	t.Run("named binary missing", func(t *testing.T) {
		for _, flag := range []string{"/nope/df", "nope-df"} {
			_, err := ResolveBinary(flag, func(string) string { return "" }, lookPath)
			if err == nil || errors.Is(err, ErrNotOnPath) {
				t.Errorf("%s: got %v, want an error that isn't ErrNotOnPath", flag, err)
			}
		}
	})
}

// A Backend with no Bin looks dataform up on PATH at each compile, so one
// opened before the CLI was installed compiles once it is.
func TestBackendFindsTheCLIOnceInstalled(t *testing.T) {
	bin := testutil.FakeCLI(t, "dataform")
	t.Setenv(BinEnv, "")
	t.Setenv("FAKE_COMPILE_FIXTURE", testdata(t, "basic.json"))
	b := Backend{}
	t.Setenv("PATH", t.TempDir())
	if _, err := b.Compile(context.Background(), t.TempDir()); !errors.Is(err, ErrNotOnPath) {
		t.Fatalf("compile without the CLI: %v, want ErrNotOnPath", err)
	}
	t.Setenv("PATH", filepath.Dir(bin))
	if _, err := b.Compile(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("compile once the CLI is on PATH: %v", err)
	}
	if got := b.RunCommand("/p", project.RunOptions{Actions: []project.ActionID{"a"}}); !strings.HasPrefix(got, "dataform run") {
		t.Errorf("RunCommand = %q, want it to start dataform run", got)
	}
}

func TestCompile(t *testing.T) {
	bin := testutil.FakeCLI(t, "dataform")
	tests := []struct {
		name       string
		fixture    string
		exit       string
		stderr     string
		wantErr    string
		wantErrors int
	}{
		{name: "clean", fixture: "basic.json"},
		{name: "errors with non-zero exit still parse", fixture: "compile_error.json", exit: "1", wantErrors: 4},
		{name: "logger prefix", fixture: "stdout_logger_prefix.txt"},
		{name: "crash", exit: "1", stderr: "\x1b[31mBoom: cannot find module\x1b[0m", wantErr: "Boom: cannot find module"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := tt.fixture
			if fixture != "" {
				fixture = testdata(t, fixture)
			}
			t.Setenv("FAKE_COMPILE_FIXTURE", fixture)
			t.Setenv("FAKE_COMPILE_EXIT", tt.exit)
			t.Setenv("FAKE_COMPILE_STDERR", tt.stderr)
			g, err := Compile(context.Background(), bin, t.TempDir())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "\x1b[") {
					t.Errorf("ANSI codes not stripped: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Errors) != tt.wantErrors {
				t.Errorf("got %d errors, want %d", len(g.Errors), tt.wantErrors)
			}
		})
	}
}

// A compile stopped by git changing .git while dataform copies the project
// is run again, a few times at most.
func TestCompileRetriesGitRace(t *testing.T) {
	bin := testutil.FakeCLI(t, "dataform")
	t.Setenv("FAKE_COMPILE_FIXTURE", testdata(t, "basic.json"))
	t.Setenv("FAKE_COMPILE_EXIT", "")
	t.Setenv("FAKE_COMPILE_STDERR", "")
	for _, c := range []struct {
		races, calls int
		ok           bool
	}{
		{races: 0, calls: 1, ok: true},
		{races: 2, calls: 3, ok: true},
		{races: 9, calls: raceAttempts},
	} {
		calls := filepath.Join(t.TempDir(), "calls")
		t.Setenv("FAKE_COMPILE_CALLS", calls)
		t.Setenv("FAKE_COMPILE_RACES", strconv.Itoa(c.races))
		_, err := Compile(context.Background(), bin, t.TempDir())
		if (err == nil) != c.ok {
			t.Errorf("%d races: err = %v", c.races, err)
		}
		if err != nil && !strings.Contains(err.Error(), "compile again") {
			t.Errorf("%d races: err = %v, want it to say to compile again", c.races, err)
		}
		if data, _ := os.ReadFile(calls); len(data) != c.calls {
			t.Errorf("%d races: %d compiles, want %d", c.races, len(data), c.calls)
		}
	}
}

// A compile that fails for another reason is not run again.
func TestCompileDoesNotRetryOtherFailures(t *testing.T) {
	bin := testutil.FakeCLI(t, "dataform")
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_COMPILE_CALLS", calls)
	t.Setenv("FAKE_COMPILE_RACES", "")
	t.Setenv("FAKE_COMPILE_FIXTURE", "")
	t.Setenv("FAKE_COMPILE_EXIT", "1")
	t.Setenv("FAKE_COMPILE_STDERR", "Error: ENOENT: no such file or directory, open '/p/definitions/a.sqlx'")
	if _, err := Compile(context.Background(), bin, t.TempDir()); err == nil {
		t.Fatal("want an error")
	}
	if data, _ := os.ReadFile(calls); len(data) != 1 {
		t.Errorf("%d compiles, want 1", len(data))
	}
}

// TestLocalProject parses a real project's compile output when it's been
// saved with `just fixtures-local`. The file is gitignored.
func TestLocalProject(t *testing.T) {
	path := testdata(t, "local", "project.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no local fixture (%v); run `just fixtures-local`", err)
	}
	var raw map[string][]json.RawMessage
	_ = json.Unmarshal(data, &raw) // non-array keys fail; the arrays we need decode

	g := loadGraph(t, filepath.Join("local", "project.json"))
	counts := map[project.ActionKind]int{}
	for _, grp := range g.Grouped() {
		counts[grp.Kind] += len(grp.Actions)
	}
	tables := counts[project.KindTable] + counts[project.KindView] + counts[project.KindIncremental]
	for _, c := range []struct {
		key  string
		want int
	}{
		{"tables", tables},
		{"operations", counts[project.KindOperation]},
		{"assertions", counts[project.KindAssertion]},
		{"declarations", counts[project.KindDeclaration]},
	} {
		if len(raw[c.key]) != c.want {
			t.Errorf("%s: graph has %d, compile output has %d", c.key, c.want, len(raw[c.key]))
		}
	}
	if counts[project.KindIncremental] == 0 {
		t.Error("expected incremental tables in the real project")
	}
	if g.HasErrors() {
		t.Errorf("unexpected compile errors: %v", g.Errors)
	}
}

func TestActionConfigMetadata(t *testing.T) {
	g := loadGraph(t, "navigation.json")
	a := g.Actions["demo-project.analytics.dev_report"]
	if a == nil {
		t.Fatal("dev_report missing")
	}
	if a.Description != "Daily report of user events" || a.PartitionBy != "DATE(created_at)" {
		t.Errorf("description %q, partition %q", a.Description, a.PartitionBy)
	}
	want := []project.ColumnDescription{
		{Path: []string{"user_id"}, Description: "The user who acted"},
		{Path: []string{"payload"}, Description: "Raw event payload"},
		{Path: []string{"payload", "kind"}, Description: "What kind of event it was"},
	}
	if len(a.Columns) != len(want) {
		t.Fatalf("columns %+v", a.Columns)
	}
	for i := range want {
		if !slices.Equal(a.Columns[i].Path, want[i].Path) || a.Columns[i].Description != want[i].Description {
			t.Errorf("column %d = %+v, want %+v", i, a.Columns[i], want[i])
		}
	}
	if a.CanonicalTarget.Name != "report" {
		t.Errorf("canonical name %q", a.CanonicalTarget.Name)
	}
}

// Clients using different Dataform CLIs never share an engine; a symlink
// is keyed on what it points at, so repointing it (an upgrade) changes it.
func TestVariant(t *testing.T) {
	dir := t.TempDir()
	old, upgraded := filepath.Join(dir, "3.0.25", "dataform"), filepath.Join(dir, "3.0.26", "dataform")
	for _, p := range []string{old, upgraded} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	variant := func(bin string) string { return Backend{Bin: bin}.Variant() }
	if v := variant(old); len(v) != 8 || v != variant(old) {
		t.Errorf("variant(%s) = %q, want 8 stable hex characters", old, v)
	}
	if variant(old) == variant(upgraded) {
		t.Errorf("%s and %s share variant %s", old, upgraded, variant(old))
	}
	link := filepath.Join(dir, "dataform")
	testutil.Symlink(t, old, link)
	if variant(link) != variant(old) {
		t.Errorf("variant(%s) = %s, want variant of its target %s = %s", link, variant(link), old, variant(old))
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	testutil.Symlink(t, upgraded, link)
	if variant(link) != variant(upgraded) {
		t.Errorf("variant(%s) didn't follow the symlink to %s", link, upgraded)
	}
}

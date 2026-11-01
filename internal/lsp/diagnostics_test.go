package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// waitDiagnostics returns the next diagnostics published for path that
// satisfy ok, keeping other messages for later.
func (c *client) waitDiagnostics(path string, ok func([]Diagnostic) bool) []Diagnostic {
	c.t.Helper()
	uri := pathURI(path)
	check := func(m rawMsg) ([]Diagnostic, bool) {
		if m.Method != "textDocument/publishDiagnostics" {
			return nil, false
		}
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(m.Params, &p); err != nil || p.URI != uri {
			return nil, false
		}
		return p.Diagnostics, ok(p.Diagnostics)
	}
	for i, m := range c.notes {
		if ds, match := check(m); match {
			c.notes = append(c.notes[:i], c.notes[i+1:]...)
			return ds
		}
	}
	for {
		m := c.next()
		if ds, match := check(m); match {
			return ds
		}
		if m.Method != "" {
			c.notes = append(c.notes, m)
		} else {
			if c.responses == nil {
				c.responses = map[string]rawMsg{}
			}
			c.responses[string(m.ID)] = m
		}
	}
}

func TestServeCompileDiagnostics(t *testing.T) {
	root := testdata(t, "projects", "compile_error")
	broken := loadGraph(t, "compile_error.json")
	fixed := *broken
	fixed.Errors = nil
	changes := make(chan struct{})
	var mu sync.Mutex
	next := broken
	c := startServer(t, Config{
		Compile: func(context.Context, string) (*project.Graph, error) {
			mu.Lock()
			defer mu.Unlock()
			return next, nil
		},
		Watch: func(context.Context, string) (<-chan struct{}, error) { return changes, nil },
	})
	c.initialize(root)
	missing := filepath.Join(root, "definitions", "missing_ref.sqlx")
	ds := c.waitDiagnostics(missing, func(ds []Diagnostic) bool { return len(ds) > 0 })
	if d := ds[0]; d.Severity != severityError || d.Range.Start != (Position{2, 21}) || d.Range.End != (Position{2, 35}) || !strings.Contains(d.Message, "does_not_exist") || d.Source != "dataform compile" {
		t.Fatalf("got %+v", ds)
	}

	mu.Lock()
	next = &fixed
	mu.Unlock()
	changes <- struct{}{}
	c.waitDiagnostics(missing, func(ds []Diagnostic) bool { return len(ds) == 0 })
}

func TestServeDryRunDiagnostics(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	var mu sync.Mutex
	var jobs []string
	cfg := fixtureConfig(t)
	cfg.DryRun = func(_ context.Context, project, location, query string) (*bigquery.DryRun, error) {
		mu.Lock()
		jobs = append(jobs, project+"/"+location)
		mu.Unlock()
		if strings.Contains(query, "dev_js_view") {
			return nil, &bigquery.APIError{Status: 404, Message: "Not found: Table demo-project:analytics.dev_js_view was not found in location EU"}
		}
		return &bigquery.DryRun{Bytes: 1 << 30, Accuracy: bigquery.AccuracyPrecise}, nil
	}
	c := startServer(t, cfg)
	c.initialize(root, encodingUTF8)
	c.open(report, "unsaved text is ignored: dry runs use what was compiled")
	ds := c.waitDiagnostics(report, func(ds []Diagnostic) bool { return len(ds) > 0 })
	if len(ds) != 1 {
		t.Fatalf("got %+v", ds)
	}
	d := ds[0]
	joinLine := 17 // JOIN ${ref("js_view")} v USING (user_id)
	if d.Severity != severityError || d.Range.Start != (Position{joinLine, 5}) || d.Range.End != (Position{joinLine, 22}) || d.Source != "bigquery dry run" {
		t.Errorf("got %+v", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(jobs) == 0 || jobs[0] != "demo-project/EU" {
		t.Errorf("dry-run jobs in %v", jobs)
	}
}

func TestServeDryRunCostHint(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	stg := filepath.Join(root, "definitions", "staging", "stg_events.sqlx")
	cfg := fixtureConfig(t)
	cfg.DryRun = func(context.Context, string, string, string) (*bigquery.DryRun, error) {
		return &bigquery.DryRun{Bytes: 3 << 30, Accuracy: bigquery.AccuracyPrecise}, nil
	}
	c := startServer(t, cfg)
	c.initialize(root)
	c.open(stg, "")
	ds := c.waitDiagnostics(stg, func(ds []Diagnostic) bool { return len(ds) > 0 })
	if len(ds) != 1 || ds[0].Severity != severityHint || ds[0].Range.Start.Line != 0 || ds[0].Message != "Dry run: 3.00 GB processed (≈ $0.0183)" {
		t.Fatalf("got %+v", ds)
	}
	// Closing the file drops its dry-run diagnostics.
	c.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": pathURI(stg)}})
	c.waitDiagnostics(stg, func(ds []Diagnostic) bool { return len(ds) == 0 })
}

func TestServeStaleDryRunsAreDropped(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	stg := filepath.Join(root, "definitions", "staging", "stg_events.sqlx")
	g := loadGraph(t, "navigation.json")
	// The recompile changes stg_events' SQL, so it is dry-run afresh rather
	// than sharing the call still in flight.
	edited := withQuery(t, g, "stg_events.sqlx", "SELECT 2")
	changes := make(chan struct{})
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	var mu sync.Mutex
	compiles := 0
	cfg := Config{
		Compile: func(context.Context, string) (*project.Graph, error) {
			mu.Lock()
			defer mu.Unlock()
			if compiles++; compiles == 1 {
				return g, nil
			}
			return edited, nil
		},
		Watch: func(context.Context, string) (<-chan struct{}, error) { return changes, nil },
	}
	cfg.DryRun = func(_ context.Context, _, _, query string) (*bigquery.DryRun, error) {
		if query != "SELECT 2" {
			started <- struct{}{}
			<-release // ignores cancellation, like a slow network
			return nil, &bigquery.APIError{Status: 400, Message: "stale error at [5:3]"}
		}
		return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise}, nil
	}
	c := startServer(t, cfg)
	c.initialize(root)
	c.open(stg, "")
	<-started
	changes <- struct{}{} // a new compile supersedes the dry run in flight
	ds := c.waitDiagnostics(stg, func(ds []Diagnostic) bool { return len(ds) > 0 })
	close(release)
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "1.00 MB") {
		t.Fatalf("got %+v", ds)
	}
	c.request("unknown/flush", nil)
	for _, m := range c.notes {
		if strings.Contains(string(m.Params), "stale error") {
			t.Fatalf("published a superseded dry run: %s", m.Params)
		}
	}
}

// TestServeDryRunsAreCached checks that a recompile that leaves a file's SQL
// alone doesn't ask BigQuery again.
func TestServeDryRunsAreCached(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	stg := filepath.Join(root, "definitions", "staging", "stg_events.sqlx")
	changes := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	cfg := fixtureConfig(t)
	cfg.Watch = func(context.Context, string) (<-chan struct{}, error) { return changes, nil }
	cfg.DryRun = func(context.Context, string, string, string) (*bigquery.DryRun, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise}, nil
	}
	c := startServer(t, cfg)
	c.initialize(root)
	c.open(stg, "")
	c.waitDiagnostics(stg, func(ds []Diagnostic) bool { return len(ds) > 0 })
	changes <- struct{}{}
	c.waitDiagnostics(stg, func(ds []Diagnostic) bool { return len(ds) > 0 })
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("BigQuery calls = %d, want 1", calls)
	}
}

// withQuery copies g with the query of the action defined in the file named
// base replaced.
func withQuery(t *testing.T, g *project.Graph, base, query string) *project.Graph {
	t.Helper()
	out := *g
	out.Actions = maps.Clone(g.Actions)
	for id, a := range g.Actions {
		if filepath.Base(a.FileName) == base && a.Query != "" {
			copied := *a
			copied.Query = query
			out.Actions[id] = &copied
			return &out
		}
	}
	t.Fatalf("no query in %s", base)
	return nil
}

func TestServeCodeAction(t *testing.T) {
	c := startServer(t, fixtureConfig(t))
	c.initialize(testdata(t, "projects", "navigation"))
	d := Diagnostic{Range: Range{Start: Position{3, 4}, End: Position{3, 14}}, Severity: severityError, Message: "Unrecognized name: TORE_ID; Did you mean STORE_ID?", Data: &DiagnosticData{Fix: "STORE_ID"}}
	plain := Diagnostic{Range: Range{Start: Position{5, 0}, End: Position{5, 3}}, Severity: severityError, Message: "Syntax error"}
	resp := c.request("textDocument/codeAction", map[string]any{
		"textDocument": map[string]any{"uri": "file:///p/a.sqlx"},
		"range":        d.Range,
		"context":      map[string]any{"diagnostics": []Diagnostic{d, plain}},
	})
	var got []CodeAction
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Replace with STORE_ID" || got[0].Kind != "quickfix" {
		t.Fatalf("got %+v", got)
	}
	edits := got[0].Edit.Changes["file:///p/a.sqlx"]
	if len(edits) != 1 || edits[0].Range != d.Range || edits[0].NewText != "STORE_ID" {
		t.Fatalf("edits %+v", edits)
	}
}

func TestServeCompletion(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	file := filepath.Join(root, "definitions", "marts", "new.sqlx")
	c := startServer(t, fixtureConfig(t))
	if caps := c.initialize(root); caps.CompletionProvider == nil || !caps.CodeActionProvider {
		t.Fatalf("capabilities %+v", caps)
	}
	text := "config { type: \"view\" }\nSELECT 'é' FROM $stg"
	c.open(file, text)
	cursor := Position{Line: 1, Character: 20} // after $stg; é is one utf-16 unit
	resp := c.request("textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(file)},
		"position":     cursor,
	})
	var list CompletionList
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatal(err)
	}
	var stg *CompletionItem
	for i := range list.Items {
		if list.Items[i].Label == "stg_events" {
			stg = &list.Items[i]
		}
	}
	if stg == nil || stg.Kind != completionKindReference || stg.FilterText != "$stg_events" || stg.Detail != "analytics · view" {
		t.Fatalf("items %+v", list.Items)
	}
	if want := (TextEdit{Range: Range{Start: Position{1, 16}, End: cursor}, NewText: `${ref("stg_events")}`}); *stg.TextEdit != want {
		t.Fatalf("edit %+v, want %+v", *stg.TextEdit, want)
	}
}

// A dbt model's dry-run errors land on the line of the .sql file that made
// the compiled SQL BigQuery complains about, with the same quick fix.
func TestServeDbtDryRunDiagnostics(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "dbt"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testdata(t, "examples", "dbt-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := (&dbt.Backend{}).Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	c := startServer(t, Config{
		Compile: func(context.Context, string) (*project.Graph, error) { return g, nil },
		DryRun: func(_ context.Context, _, _, query string) (*bigquery.DryRun, error) {
			i := strings.Index(query, "amount) as lifetime_value")
			if i < 0 {
				return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise}, nil
			}
			line, col := strings.Count(query[:i], "\n")+1, i-strings.LastIndex(query[:i], "\n")
			return nil, &bigquery.APIError{Status: 400, Message: fmt.Sprintf("Unrecognized name: amount; Did you mean amounts? at [%d:%d]", line, col)}
		},
		Dbt: func(r string) bool { return r == root },
	})
	c.initialize(root, encodingUTF8)
	model := filepath.Join(root, "models", "marts", "dim_customers.sql")
	c.open(model, "")
	ds := c.waitDiagnostics(model, func(ds []Diagnostic) bool { return len(ds) > 0 })
	var errs []Diagnostic
	for _, d := range ds {
		if d.Severity == severityError {
			errs = append(errs, d)
		}
	}
	// "    sum(p.amount) as lifetime_value" is line 15.
	if len(errs) != 1 || errs[0].Range != (Range{Start: Position{15, 10}, End: Position{15, 16}}) || errs[0].Source != "bigquery dry run" || errs[0].Data == nil || errs[0].Data.Fix != "amounts" {
		t.Fatalf("got %+v", ds)
	}
}

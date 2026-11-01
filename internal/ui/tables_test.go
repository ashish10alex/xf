package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeTables answers table lookups from canned replies by table name;
// anything else doesn't exist.
type fakeTables struct {
	mu      sync.Mutex
	replies map[string]func() (*bigquery.Table, error)
	calls   []string // "project.dataset.table"
}

func (f *fakeTables) table(_ context.Context, project, dataset, table string) (*bigquery.Table, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, project+"."+dataset+"."+table)
	if reply := f.replies[table]; reply != nil {
		return reply()
	}
	return nil, &bigquery.APIError{Status: 404, Message: "Not found: Table " + table}
}

func (f *fakeTables) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func TestActionsPaneShowsWhenTheTableWasUpdated(t *testing.T) {
	now := testNow()
	updated := now.Add(-3 * time.Hour)
	var failing error
	bq := &fakeTables{replies: map[string]func() (*bigquery.Table, error){
		"daily_orders": func() (*bigquery.Table, error) {
			if failing != nil {
				return nil, failing
			}
			return &bigquery.Table{Type: "TABLE", LastModified: updated}, nil
		},
	}}
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Table: bq.table, Now: func() time.Time { return now }, Editor: make(chan project.EditorEvent)})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 140, Height: 40})
	m = closePalette(t, m)
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))

	const table = "demo-project.analytics.daily_orders"
	want := "    updated      " + updated.Local().Format("2006-01-02 15:04") + " · 3h ago"
	assertContains(t, screen(m), want)
	if n := bq.count(table); n != 1 {
		t.Fatalf("%d lookups of the table, want 1: %q", n, bq.calls)
	}

	// The answer is kept for a while, then asked for again.
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/weekly_revenue.sqlx"))
	// A table that hasn't been built is N/A, dimmed.
	if view := screen(m); !strings.Contains(view, "    updated      N/A") {
		t.Errorf("no N/A for a table that doesn't exist:\n%s", view)
	}
	if d, ok := m.updatedDetail(m.selected); !ok || d.value != "N/A" || d.style.GetForeground() != m.styles.dim.GetForeground() {
		t.Errorf("N/A isn't dim: %+v", d)
	}
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	if n := bq.count(table); n != 1 {
		t.Errorf("%d lookups after switching back, want 1", n)
	}
	now = now.Add(2 * tableMaxAge)
	updated = now.Add(-10 * time.Second)
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 140, Height: 40})
	if n := bq.count(table); n != 2 {
		t.Errorf("%d lookups once the answer was old, want 2", n)
	}
	assertContains(t, screen(m), " · just now")

	if d, _ := m.updatedDetail(m.selected); d.style.GetForeground() != m.styles.text.GetForeground() {
		t.Errorf("a table updated today isn't in the text colour: %+v", d)
	}

	// A table last updated before today is red.
	updated = now.Add(-48 * time.Hour)
	m.expireTables(false)
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 140, Height: 40})
	assertContains(t, screen(m), updated.Local().Format("2006-01-02 15:04")+" · 2d ago")
	if d, _ := m.updatedDetail(m.selected); d.style.GetForeground() != m.styles.errorText.GetForeground() {
		t.Errorf("a table updated before today isn't red: %+v", d)
	}

	// A failed lookup says why.
	failing = &bigquery.APIError{Status: 403, Message: "Access Denied: Table daily_orders"}
	m.expireTables(false)
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 140, Height: 40})
	assertContains(t, screen(m), "    updated      ✗ Access Denied: Table daily_orders")
}

func TestNoUpdatedLineWithoutDeps(t *testing.T) {
	m, _ := newDryRunModel(t, &fakeBigQuery{}, "basic.json")
	m = drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx"))
	if view := screen(m); strings.Contains(view, "updated") {
		t.Errorf("an updated line without table lookups:\n%s", view)
	}
}

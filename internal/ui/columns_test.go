package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ashish10alex/xf/internal/bigquery"
)

// returns is a dry run of a query with these columns, each "name TYPE" or
// "name TYPE MODE".
func returns(columns ...string) func() (*bigquery.DryRun, error) {
	return func() (*bigquery.DryRun, error) {
		d := &bigquery.DryRun{Bytes: 1024, Accuracy: bigquery.AccuracyPrecise, StatementType: "SELECT"}
		for _, c := range columns {
			f := strings.Fields(c)
			field := bigquery.Field{Name: f[0], Type: f[1]}
			if len(f) > 2 {
				field.Mode = f[2]
			}
			d.Schema = append(d.Schema, field)
		}
		return d, nil
	}
}

func newColumnsModel(t *testing.T, replies map[string]func() (*bigquery.DryRun, error)) (Model, *[]string) {
	t.Helper()
	m, _ := newDryRunModel(t, &fakeBigQuery{replies: replies}, "basic.json")
	copied := &[]string{}
	m.deps.Copy = func(s string) error { *copied = append(*copied, s); return nil }
	return drive(t, m, nil, editorEvent("BufEnter", "definitions/daily_orders.sqlx")), copied
}

var dailyOrders = map[string]func() (*bigquery.DryRun, error){
	"SUM(": returns("region STRING REQUIRED", "order_date DATE NULLABLE", "Orders INTEGER", "items RECORD REPEATED", "revenue NUMERIC"),
}

func TestColumnsViewListsTheDryRunSchemaByName(t *testing.T) {
	m, _ := newColumnsModel(t, dailyOrders)
	assertNotContains(t, screen(m), "│ columns")

	// S swaps the pane's view from any pane, and leaves the focus.
	m = drive(t, m, nil, press("1", "S")...)
	if m.focus != paneActions {
		t.Errorf("S moved the focus to pane %d", m.focus)
	}
	view := screen(m)
	assertContains(t, view, "0 sql │ columns · analytics.daily_orders · table", "5 columns",
		"Column", "Type", "Mode", "region", "STRING", "REQUIRED", "REPEATED")
	assertNotContains(t, view, "NULLABLE", "SELECT")
	var at []int
	for _, name := range []string{" items ", " order_date ", " Orders ", " region ", " revenue "} {
		at = append(at, strings.Index(view, name))
	}
	for i := 1; i < len(at); i++ {
		if at[i-1] < 0 || at[i] < at[i-1] {
			t.Fatalf("columns not sorted by name (offsets %v):\n%s", at, view)
		}
	}

	// The view stays as the selection moves, and S swaps back.
	m = drive(t, m, nil, press("j")...)
	assertContains(t, screen(m), "0 sql │ columns")
	m = drive(t, m, nil, press("S")...)
	assertNotContains(t, screen(m), "│ columns")
}

func TestColumnsViewExpandsRecords(t *testing.T) {
	m, copied := newColumnsModel(t, map[string]func() (*bigquery.DryRun, error){
		"SUM(": func() (*bigquery.DryRun, error) {
			return &bigquery.DryRun{StatementType: "SELECT", Schema: []bigquery.Field{
				{Name: "order_id", Type: "INTEGER"},
				{Name: "items", Type: "RECORD", Mode: "REPEATED", Fields: []bigquery.Field{
					{Name: "sku", Type: "STRING"},
					{Name: "price", Type: "RECORD", Fields: []bigquery.Field{{Name: "amount", Type: "NUMERIC"}}},
				}},
				{Name: "items_count", Type: "INTEGER"},
			}}, nil
		},
	})
	m = drive(t, m, nil, press("S")...)
	view := screen(m)
	assertContains(t, view, "6 columns")
	at := -1
	for _, name := range []string{" items ", " items.price ", " items.price.amount ", " items.sku ", " items_count ", " order_id "} {
		i := strings.Index(view, name)
		if i < at || i < 0 {
			t.Fatalf("%q missing or out of order:\n%s", name, view)
		}
		at = i
	}
	// A nested field filters and copies by its path.
	m = drive(t, m, nil, append(append(press("/"), typeText("sku")...), press("enter", "y")...)...)
	assertContains(t, screen(m), "1 of 6 columns")
	if len(*copied) != 1 || (*copied)[0] != "items.sku" {
		t.Errorf("copied %q", *copied)
	}
}

func TestColumnsViewFiltersByNameAndType(t *testing.T) {
	m, _ := newColumnsModel(t, dailyOrders)
	m = drive(t, m, nil, press("S")...)
	assertContains(t, screen(m), "/ filter")

	// / filters by name; while typing, letters are the filter's.
	m = drive(t, m, nil, append(press("/"), typeText("OR")...)...)
	view := screen(m)
	assertContains(t, view, "2 of 5 columns", " order_date ", " Orders ", "tab name/type")
	assertNotContains(t, view, " region ", " revenue ")
	if !m.cols.shown || m.palette.open {
		t.Fatal("typing a filter ran the keys' commands")
	}

	// ctrl+n and ctrl+p move the cursor among the matches while typing.
	m = drive(t, m, nil, press("ctrl+n")...)
	if m.cols.cursor != 1 || m.cols.name.Value() != "OR" {
		t.Errorf("ctrl+n: cursor %d, filter %q", m.cols.cursor, m.cols.name.Value())
	}
	m = drive(t, m, nil, press("ctrl+n", "ctrl+p", "ctrl+p")...)
	if m.cols.cursor != 0 {
		t.Errorf("ctrl+p: cursor %d", m.cols.cursor)
	}

	// tab moves to the type's box; both filters must match.
	m = drive(t, m, nil, append(press("tab"), typeText("int")...)...)
	view = screen(m)
	assertContains(t, view, "1 of 5 columns", " Orders ", "/OR", "/int")
	assertNotContains(t, view, " order_date ")

	// enter keeps the filters and returns the keys to the table.
	m = drive(t, m, nil, press("enter")...)
	assertContains(t, screen(m), "1 of 5 columns", "esc clear filter")
	m = drive(t, m, nil, press("esc")...)
	view = screen(m)
	assertContains(t, view, "5 columns", " region ")
	assertNotContains(t, view, " of 5 columns")

	// A filter that matches nothing says so.
	m = drive(t, m, nil, append(press("/"), typeText("zzz")...)...)
	assertContains(t, screen(m), "no column matches", "0 of 5 columns")

	// Another action starts unfiltered.
	m = drive(t, m, nil, press("enter", "]")...)
	if m.cols.filtered() {
		t.Error("the filter outlived its action")
	}
}

func TestColumnsViewCopiesTheColumnUnderTheCursor(t *testing.T) {
	m, copied := newColumnsModel(t, dailyOrders)
	m = drive(t, m, nil, press("S", "j", "y", "Y")...)
	want := []string{"order_date", "order_date DATE"}
	if len(*copied) != 2 || (*copied)[0] != want[0] || (*copied)[1] != want[1] {
		t.Fatalf("copied %q, want %q", *copied, want)
	}
	assertContains(t, screen(m), "copied order_date DATE")

	// G and g move to the ends; the wheel and a click move the cursor too.
	m = drive(t, m, nil, press("G")...)
	if m.cols.cursor != 4 {
		t.Errorf("G put the cursor on %d", m.cols.cursor)
	}
	m = drive(t, m, nil, press("g")...)
	var sql paneRect
	for _, r := range m.paneRects() {
		if r.id == paneSQL {
			sql = r
		}
	}
	m = drive(t, m, nil, wheel(5, sql.y+2, true))
	if m.cols.cursor != 1 {
		t.Errorf("the wheel put the cursor on %d", m.cols.cursor)
	}
	m = drive(t, m, nil, click(5, sql.y+1+1+3)) // border, header, fourth column
	if m.cols.cursor != 3 {
		t.Errorf("the click put the cursor on %d", m.cols.cursor)
	}
}

func TestColumnsViewSaysWhyThereAreNone(t *testing.T) {
	m, _ := newColumnsModel(t, map[string]func() (*bigquery.DryRun, error){
		"SUM(": fails(&bigquery.APIError{Status: 400, Message: "Unrecognized name: foo at [3:5]\nmore"}),
	})
	m = drive(t, m, nil, press("S")...)
	view := screen(m)
	assertContains(t, view, "✗ Unrecognized name: foo at [3:5] — press e for details")
	assertNotContains(t, view, "Mode", "/ filter")
	// With no table to filter, / finds an action as elsewhere.
	m = drive(t, m, nil, press("/")...)
	if !m.palette.open {
		t.Error("/ did not open the palette")
	}
	m = closePalette(t, m)

	// An action with nothing to dry run.
	for _, grp := range m.graph.Grouped() {
		for _, a := range grp.Actions {
			if len(a.DryRunQueries()) == 0 {
				m = startOn(t, m, a.ID)
				assertContains(t, screen(m), "nothing to dry run, so no columns")
				return
			}
		}
	}
	t.Fatal("basic.json has no action without a dry run")
}

func TestColumnsViewFollowsTheIncrementalVariant(t *testing.T) {
	bq := &fakeBigQuery{replies: map[string]func() (*bigquery.DryRun, error){
		"MAX(event_date)":   returns("event_date DATE", "late_only INTEGER"),
		"DATE '2000-01-01'": returns("event_date DATE", "full_only INTEGER"),
	}}
	m, _ := newDryRunModel(t, bq, "incremental.json")
	m = startOn(t, m, "demo-project.reporting.daily_events")
	m = drive(t, m, nil, tea.WindowSizeMsg{Width: 140, Height: 40}, keyMsg("S"))
	view := screen(m)
	assertContains(t, view, "[incremental]", "late_only")
	assertNotContains(t, view, "full_only")
	m = drive(t, m, nil, press("i")...)
	view = screen(m)
	assertContains(t, view, "[full]", "full_only")
	assertNotContains(t, view, "late_only")
}

func TestColumnsCommandInThePalette(t *testing.T) {
	m, _ := newColumnsModel(t, dailyOrders)
	m = drive(t, m, nil, append(append(press("/"), typeText(">view: columns")...), press("enter")...)...)
	if !m.cols.shown {
		t.Error("the palette's command did not show the columns")
	}
}

package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func testBuffer(lines ...string) *buffer {
	b := &buffer{}
	plain := make([][]rune, len(lines))
	for i, l := range lines {
		plain[i] = []rune(l)
	}
	b.setHeight(10)
	b.setWidth(40)
	b.setText(lines, plain, true)
	return b
}

func TestBufferWordMotions(t *testing.T) {
	b := testBuffer("SELECT a.b_c, 42", "", "  FROM x")
	type pos struct{ row, col int }
	var got []pos
	for range 7 {
		b.wordForward(false)
		got = append(got, pos{b.row, b.col})
	}
	want := []pos{{0, 7}, {0, 8}, {0, 9}, {0, 12}, {0, 14}, {1, 0}, {2, 2}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("w steps = %v, want %v", got, want)
		}
	}
	for i := len(want) - 2; i >= 0; i-- {
		b.wordBackward(false)
		if (pos{b.row, b.col}) != want[i] {
			t.Fatalf("b step to %v, want %v", pos{b.row, b.col}, want[i])
		}
	}

	b = testBuffer("SELECT a.b_c, 42")
	b.wordForward(true)
	b.wordForward(true)
	if b.col != 14 {
		t.Errorf("W W col = %d, want 14", b.col)
	}
	b.wordBackward(true)
	if b.col != 7 {
		t.Errorf("B col = %d, want 7", b.col)
	}
}

func TestBufferVerticalMoveKeepsColumn(t *testing.T) {
	b := testBuffer("abcdefgh", "ab", "abcdefgh")
	b.setCol(6)
	b.moveRow(1)
	if b.col != 1 {
		t.Fatalf("col on short line = %d, want 1", b.col)
	}
	b.moveRow(1)
	if b.col != 6 {
		t.Errorf("col after the short line = %d, want 6", b.col)
	}
}

func TestBufferHorizontalScroll(t *testing.T) {
	long := "SELECT " + strings.Repeat("x", 60) + " END"
	b := testBuffer(long)
	b.setCol(len(long))
	if b.xoff == 0 {
		t.Fatal("cursor at the end of a long line did not scroll sideways")
	}
	view := ansi.Strip(b.view(newStyles(true), true)[0])
	if !strings.HasSuffix(strings.TrimRight(view, " "), "END") {
		t.Errorf("scrolled view = %q, want it to end with END", view)
	}
	b.setCol(0)
	view = ansi.Strip(b.view(newStyles(true), true)[0])
	if b.xoff != 0 || !strings.HasPrefix(view, "1 SELECT") || !strings.HasSuffix(view, "…") {
		t.Errorf("view at the line start = %q (xoff %d)", view, b.xoff)
	}
}

// sqlModel is a test model with daily_orders selected, the SQL pane focused
// and copies captured.
func sqlModel(t *testing.T) (Model, *[]string) {
	t.Helper()
	m, _ := newTestModel(t, "basic.json")
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	copied := &[]string{}
	m.deps.Copy = func(s string) error {
		*copied = append(*copied, s)
		return nil
	}
	return drive(t, m, nil, press("0")...), copied
}

func lastCopy(t *testing.T, copied *[]string) string {
	t.Helper()
	if len(*copied) == 0 {
		t.Fatal("nothing was copied")
	}
	return (*copied)[len(*copied)-1]
}

func TestSQLVisualYank(t *testing.T) {
	m, copied := sqlModel(t)
	// Line 2 is "  DATE(o.created_at) AS order_date,".
	m = drive(t, m, nil, press("j", "j", "^", "v")...)
	if m.sql.visual != visualChar {
		t.Fatalf("v did not start visual mode (visual = %v)", m.sql.visual)
	}
	m = drive(t, m, nil, press("w", "w", "y")...)
	if got := lastCopy(t, copied); got != "DATE(o" {
		t.Errorf("copied %q, want %q", got, "DATE(o")
	}
	if m.sql.visual != visualNone || m.sql.row != 2 || m.sql.col != 2 {
		t.Errorf("after y: visual = %v, cursor = %d,%d; want normal mode at the selection start", m.sql.visual, m.sql.row, m.sql.col)
	}
	assertContains(t, screen(m), "copied 6 characters")

	// Charwise across lines, then linewise.
	m = drive(t, m, nil, press("$", "v", "j", "0", "y")...)
	if got := lastCopy(t, copied); got != ",\n " {
		t.Errorf("copied %q across lines", got)
	}
	m = drive(t, m, nil, press("V", "j", "y")...)
	if got := lastCopy(t, copied); got != "  DATE(o.created_at) AS order_date,\n  c.region," {
		t.Errorf("linewise copy = %q", got)
	}
	m = drive(t, m, nil, press("G", "y", "y")...)
	if got := lastCopy(t, copied); got != "GROUP BY 1, 2" {
		t.Errorf("yy copied %q", got)
	}
	assertContains(t, screen(m), "copied 1 line")
}

func TestSQLVimKeysWinOverGlobals(t *testing.T) {
	m, _ := newTestModel(t, "incremental.json")
	m = startOn(t, m, "demo-project.reporting.daily_events")
	m = drive(t, m, nil, press("0", "v")...)
	if !m.incremental || m.sql.visual != visualChar {
		t.Fatalf("v in the SQL pane: incremental = %v, visual = %v", m.incremental, m.sql.visual)
	}
	// esc leaves visual mode before it unzooms.
	m = drive(t, m, nil, press("z", "esc")...)
	if !m.zoomed || m.sql.visual != visualNone {
		t.Fatalf("esc: zoomed = %v, visual = %v", m.zoomed, m.sql.visual)
	}
	m = drive(t, m, nil, press("esc", "l", "l", "0")...)
	if m.zoomed || m.sql.col != 0 || m.focus != paneSQL {
		t.Errorf("0 in the SQL pane: col = %d, focus = %v", m.sql.col, m.focus)
	}
	// i toggles incremental from anywhere; v still does outside the SQL pane.
	m = drive(t, m, nil, press("i")...)
	if m.incremental {
		t.Error("i did not toggle incremental in the SQL pane")
	}
	m = drive(t, m, nil, press("2", "v")...)
	if !m.incremental {
		t.Error("v did not toggle incremental in the actions pane")
	}
}

func TestBufferCursorStaysInView(t *testing.T) {
	b := testBuffer("0", "1", "2", "3", "4", "5", "6", "7", "8")
	b.setHeight(3)
	b.gotoRow(8)
	if b.row != 8 || b.offset != 6 {
		t.Errorf("G: row = %d, offset = %d", b.row, b.offset)
	}
	b.scrollView(-6)
	if b.offset != 0 || b.row != 2 {
		t.Errorf("scrolling up left the cursor on row %d (offset %d), want 2", b.row, b.offset)
	}
	b.halfPage(1)
	if b.offset != 1 || b.row != 3 {
		t.Errorf("ctrl+d: row = %d, offset = %d", b.row, b.offset)
	}
}

func TestSQLMouseDragSelects(t *testing.T) {
	m, copied := sqlModel(t)
	r := rectOf(t, m, paneSQL)
	gutter := m.sql.gutterWidth()
	// Row 1 inside the pane is line 2 ("SELECT"); drag over "SEL".
	x0, y := r.x+1+gutter, r.y+2
	m = drive(t, m, nil,
		tea.MouseClickMsg{X: x0, Y: y, Button: tea.MouseLeft},
		tea.MouseMotionMsg{X: x0 + 2, Y: y, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: x0 + 2, Y: y, Button: tea.MouseLeft},
	)
	if m.sql.visual != visualChar {
		t.Fatal("dragging did not select")
	}
	m = drive(t, m, nil, press("y")...)
	if got := lastCopy(t, copied); got != "SEL" {
		t.Errorf("copied %q, want SEL", got)
	}
	// A plain click moves the cursor without selecting.
	m = drive(t, m, nil, tea.MouseClickMsg{X: x0 + 3, Y: y + 1, Button: tea.MouseLeft}, tea.MouseReleaseMsg{X: x0 + 3, Y: y + 1})
	if m.sql.visual != visualNone || m.sql.row != 2 || m.sql.col != 3 {
		t.Errorf("click: visual = %v, cursor = %d,%d", m.sql.visual, m.sql.row, m.sql.col)
	}
}

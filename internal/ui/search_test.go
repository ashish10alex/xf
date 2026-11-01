package ui

import (
	"strings"
	"testing"
)

func TestSearchPatternFind(t *testing.T) {
	text := [][]rune{[]rune("SELECT id, customer_id"), []rune("FROM Orders -- orders, ID")}
	cases := []struct {
		name string
		p    searchPattern
		want []hit
	}{
		{"lower case ignores case", searchPattern{text: "orders"}, []hit{{1, 5, 11}, {1, 15, 21}}},
		{"upper case is exact", searchPattern{text: "Orders"}, []hit{{1, 5, 11}}},
		{"substring", searchPattern{text: "id"}, []hit{{0, 7, 9}, {0, 20, 22}, {1, 23, 25}}},
		{"whole word, any case", searchPattern{text: "id", word: true}, []hit{{0, 7, 9}, {1, 23, 25}}},
		{"whole word of an upper-case word", searchPattern{text: "ID", word: true}, []hit{{0, 7, 9}, {1, 23, 25}}},
		{"no overlap", searchPattern{text: "aa"}, nil},
		{"empty", searchPattern{}, nil},
	}
	for _, c := range cases {
		got := c.p.find(text)
		if len(got) != len(c.want) {
			t.Errorf("%s: hits = %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: hits = %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
	if got := (searchPattern{text: "aa"}).find([][]rune{[]rune("aaaaa")}); len(got) != 2 {
		t.Errorf("aa in aaaaa = %v, want 2 hits that don't overlap", got)
	}
}

func TestBufferSearchSteps(t *testing.T) {
	b := testBuffer("a x", "x b", "c x")
	b.setSearch(searchPattern{text: "x"})
	type step struct {
		forward       bool
		row, col      int
		wantedWrapped bool
	}
	for _, s := range []step{{true, 0, 2, false}, {true, 1, 0, false}, {true, 2, 2, false}, {true, 0, 2, true},
		{false, 2, 2, true}, {false, 1, 0, false}} {
		i, wrapped := b.nextHit(s.forward)
		b.gotoHit(i)
		if b.row != s.row || b.col != s.col || wrapped != s.wantedWrapped {
			t.Fatalf("step %+v: at %d,%d wrapped %v", s, b.row, b.col, wrapped)
		}
	}
	if b.curHit() != 1 {
		t.Errorf("curHit = %d, want 1", b.curHit())
	}
	b.setCol(1)
	if b.curHit() != -1 {
		t.Errorf("curHit off a match = %d, want -1", b.curHit())
	}

	// New text keeps the pattern and finds it again.
	b.setText([]string{"x x"}, [][]rune{[]rune("x x")}, true)
	if len(b.hits) != 2 || !b.hlHits {
		t.Errorf("after setText: hits %v, highlighted %v", b.hits, b.hlHits)
	}
}

func TestBufferSearchExtendsVisual(t *testing.T) {
	b := testBuffer("select a", "from t")
	b.toggleVisual(visualChar)
	b.setSearch(searchPattern{text: "from"})
	i, _ := b.nextHit(true)
	b.gotoHit(i)
	if got, _ := b.yankVisual(); got != "select a\nf" {
		t.Errorf("v/from y = %q", got)
	}
}

func TestBufferWordAtCursor(t *testing.T) {
	b := testBuffer("  a.b_c, 42")
	for col, want := range map[int]string{0: "a", 3: ".", 4: "b_c", 6: "b_c", 7: "42"} {
		if want == "." {
			want = "b_c" // on punctuation, the next word
		}
		b.setCol(col)
		if got, _ := b.wordAtCursor(); got != want {
			t.Errorf("word at %d = %q, want %q", col, got, want)
		}
	}
	b = testBuffer("a ;")
	b.setCol(2)
	if got, _ := b.wordAtCursor(); got != "" {
		t.Errorf("word after the last one = %q", got)
	}
}

// TestSQLSearch: / searches the zoomed SQL (and finds an action otherwise),
// n and N step around the ends, esc clears the highlights before it unzooms,
// and the pattern outlives the action.
func TestSQLSearch(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = find(t, m, "weekly")
	sql := strings.ToLower(plainText(m.sql.text))
	total := strings.Count(sql, "date_trunc")
	if total == 0 {
		t.Fatal("fixture has no DATE_TRUNC")
	}

	// Unzoomed, / is still the palette.
	if pm := drive(t, m, nil, press("/")...); !pm.palette.open || pm.search.typing {
		t.Fatalf("unzoomed /: palette %v, search %v", pm.palette.open, pm.search.typing)
	}

	// Zoomed, it searches: the cursor follows the query as it is typed.
	m = drive(t, m, nil, append(press("z", "/"), typeText("date_tr")...)...)
	if m.palette.open || !m.search.typing {
		t.Fatalf("zoomed /: palette %v, search %v", m.palette.open, m.search.typing)
	}
	assertContains(t, screen(m), "/date_tr")
	if m.sql.curHit() != 0 {
		t.Errorf("typing did not go to the first match: cursor %d,%d", m.sql.row, m.sql.col)
	}
	// Letters are the query's, not keys: q doesn't quit.
	m = drive(t, m, nil, append(typeText("q"), press("backspace")...)...)
	if m.confirmQuit || m.search.input.Value() != "date_tr" {
		t.Fatalf("q while typing: query %q", m.search.input.Value())
	}

	// esc puts the cursor back and drops the query.
	back := drive(t, m, nil, press("esc")...)
	if back.search.typing || back.sql.row != 0 || back.sql.col != 0 || back.sql.hlHits || !back.zoomed {
		t.Errorf("esc: cursor %d,%d, highlighted %v, zoomed %v", back.sql.row, back.sql.col, back.sql.hlHits, back.zoomed)
	}

	m = drive(t, m, nil, press("enter")...)
	if m.search.typing || m.sql.curHit() != 0 {
		t.Fatalf("enter: typing %v, cursor %d,%d", m.search.typing, m.sql.row, m.sql.col)
	}
	assertContains(t, screen(m), "/date_tr · 1 of ")

	// n goes around the ends and says so.
	for range total {
		m = drive(t, m, nil, press("n")...)
	}
	if m.sql.curHit() != 0 || !strings.Contains(m.flash, "continuing at top") {
		t.Errorf("n past the last match: hit %d, flash %q", m.sql.curHit(), m.flash)
	}
	m = drive(t, m, nil, press("N")...)
	if m.sql.curHit() != total-1 || !strings.Contains(m.flash, "continuing at bottom") {
		t.Errorf("N before the first match: hit %d, flash %q", m.sql.curHit(), m.flash)
	}

	// esc hides the highlights, then unzooms; n shows them again, unzoomed.
	m = drive(t, m, nil, press("esc")...)
	if m.sql.hlHits || !m.zoomed {
		t.Fatalf("first esc: highlighted %v, zoomed %v", m.sql.hlHits, m.zoomed)
	}
	m = drive(t, m, nil, press("esc")...)
	if m.zoomed {
		t.Fatal("second esc did not unzoom")
	}
	m = drive(t, m, nil, press("n")...)
	if !m.sql.hlHits || m.sql.curHit() < 0 {
		t.Errorf("n after esc: highlighted %v, hit %d", m.sql.hlHits, m.sql.curHit())
	}

	// An empty query repeats the last pattern.
	m = drive(t, m, nil, press("g", "z", "/", "enter")...)
	if m.sql.search.text != "date_tr" || m.sql.curHit() != 0 {
		t.Errorf("empty query: pattern %q, hit %d", m.sql.search.text, m.sql.curHit())
	}

	// A query nothing matches leaves the cursor where it was.
	at := m.sql.pos()
	m = drive(t, m, nil, append(append(press("/"), typeText("zzzz")...), press("enter")...)...)
	if m.sql.pos() != at || m.flash != "not found: zzzz" {
		t.Errorf("no match: cursor moved %v, flash %q", m.sql.pos() != at, m.flash)
	}

	// The pattern is still there for another action's SQL.
	m = drive(t, m, nil, append(append(press("/"), typeText("select")...), press("enter", "esc", "esc")...)...)
	m = find(t, m, "dord")
	if m.sql.search.text != "select" || len(m.sql.hits) == 0 {
		t.Errorf("after an action change: pattern %q, %d hits", m.sql.search.text, len(m.sql.hits))
	}
}

// TestSQLSearchWord: * and # search the word under the cursor, unzoomed too,
// and N goes against the direction of the search.
func TestSQLSearchWord(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = find(t, m, "weekly")
	// The first line is the section's "-- title"; search a word of the SQL.
	m = drive(t, m, nil, press("j", "*")...)
	word, _ := m.sql.wordAtCursor()
	if !m.sql.search.word || !strings.EqualFold(m.sql.search.text, word) || m.zoomed {
		t.Fatalf("*: pattern %+v, word under the cursor %q", m.sql.search, word)
	}
	first := m.sql.pos()
	m = drive(t, m, nil, press("#")...)
	if !m.sql.search.back {
		t.Fatal("# did not search backward")
	}
	// N after # goes forward again.
	m = drive(t, m, nil, press("N")...)
	if len(m.sql.hits) > 1 && m.sql.pos() != first {
		t.Errorf("# then N: at %+v, want %+v", m.sql.pos(), first)
	}
}

// TestSQLSearchCommand: the palette's command zooms the SQL and opens the
// query line, from any pane.
func TestSQLSearchCommand(t *testing.T) {
	m, _ := newTestModel(t, "basic.json")
	m = find(t, m, "weekly")
	m = drive(t, m, nil, press("1")...)
	m = query(t, m, ">sql search")
	if c := m.palette.selectedCommand(); c == nil || c.title != "SQL: search" {
		t.Fatalf("first match = %+v", c)
	}
	m = drive(t, m, nil, press("enter")...)
	if m.focus != paneSQL || !m.zoomed || !m.search.typing {
		t.Errorf("focus %v, zoomed %v, typing %v", m.focus, m.zoomed, m.search.typing)
	}
}

func plainText(lines [][]rune) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(string(l) + "\n")
	}
	return b.String()
}

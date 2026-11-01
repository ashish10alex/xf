package ui

import (
	"fmt"
	"sort"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// searchPattern is what a buffer searches for, as vim's / and * do. A typed
// pattern is plain text with smartcase: case matters only when it has an
// upper-case letter.
type searchPattern struct {
	text string
	word bool // * and #: whole words only, whatever their case
	back bool // # searched backward, so n goes backward and N forward
}

// hit is a match: runes [from, to) of a line.
type hit struct{ row, from, to int }

// find lists the pattern's matches in text, in order and not overlapping.
func (p searchPattern) find(text [][]rune) []hit {
	q := []rune(p.text)
	if len(q) == 0 {
		return nil
	}
	fold := p.word
	if !fold {
		fold = true
		for _, r := range q {
			if unicode.IsUpper(r) {
				fold = false
			}
		}
	}
	if fold {
		for i, r := range q {
			q[i] = unicode.ToLower(r)
		}
	}
	var hits []hit
	for row, l := range text {
	next:
		for c := 0; c+len(q) <= len(l); c++ {
			for i, r := range q {
				lr := l[c+i]
				if fold {
					lr = unicode.ToLower(lr)
				}
				if lr != r {
					continue next
				}
			}
			end := c + len(q)
			if p.word && ((c > 0 && charClass(l[c-1], false) == 1) || (end < len(l) && charClass(l[end], false) == 1)) {
				continue
			}
			hits = append(hits, hit{row, c, end})
			c = end - 1
		}
	}
	return hits
}

// setSearch makes p the buffer's pattern and highlights its matches.
func (b *buffer) setSearch(p searchPattern) {
	b.search = p
	b.hits = p.find(b.text)
	b.hlHits = p.text != ""
}

// nextHit is the first match after the cursor (before it when !forward),
// going around the ends. The buffer must have a match.
func (b *buffer) nextHit(forward bool) (i int, wrapped bool) {
	n := len(b.hits)
	if forward {
		i = sort.Search(n, func(i int) bool {
			h := b.hits[i]
			return h.row > b.row || (h.row == b.row && h.from > b.col)
		})
		if i == n {
			return 0, true
		}
		return i, false
	}
	i = sort.Search(n, func(i int) bool {
		h := b.hits[i]
		return h.row > b.row || (h.row == b.row && h.from >= b.col)
	}) - 1
	if i < 0 {
		return n - 1, true
	}
	return i, false
}

// curHit is the match the cursor is at the start of, or -1.
func (b *buffer) curHit() int {
	i := sort.Search(len(b.hits), func(i int) bool {
		h := b.hits[i]
		return h.row > b.row || (h.row == b.row && h.from >= b.col)
	})
	if i < len(b.hits) && b.hits[i].row == b.row && b.hits[i].from == b.col {
		return i
	}
	return -1
}

// rowHits are the matches on a line.
func (b *buffer) rowHits(row int) []hit {
	from := sort.Search(len(b.hits), func(i int) bool { return b.hits[i].row >= row })
	to := from
	for to < len(b.hits) && b.hits[to].row == row {
		to++
	}
	return b.hits[from:to]
}

// wordAtCursor is the keyword under the cursor, or the next one on its
// line, as vim's * takes it, and the column it starts at.
func (b *buffer) wordAtCursor() (string, int) {
	l, c := b.line(), b.col
	for c < len(l) && charClass(l[c], false) != 1 {
		c++
	}
	if c >= len(l) {
		return "", 0
	}
	from, to := c, c
	for from > 0 && charClass(l[from-1], false) == 1 {
		from--
	}
	for to < len(l) && charClass(l[to], false) == 1 {
		to++
	}
	return string(l[from:to]), from
}

// bufferPos is where a buffer's cursor and view are.
type bufferPos struct{ row, col, want, offset, xoff int }

func (b *buffer) pos() bufferPos { return bufferPos{b.row, b.col, b.want, b.offset, b.xoff} }

// restore puts the cursor and view back, within the text there is now.
func (b *buffer) restore(p bufferPos) {
	b.row = min(p.row, max(len(b.text)-1, 0))
	b.col, b.want, b.offset, b.xoff = p.col, p.want, p.offset, p.xoff
	b.clampCol()
	b.clamp()
}

// sqlSearch is the SQL pane's query line. "/" in the zoomed pane searches
// the SQL as vim does: every match is highlighted and the cursor goes to
// the next one as the query is typed; enter keeps it there and esc puts it
// back. The pattern itself is the buffer's, so n, N, * and # work unzoomed
// and it outlives the action shown.
type sqlSearch struct {
	input  textinput.Model
	typing bool          // "/" was pressed: keys edit the query until enter or esc
	from   bufferPos     // where the cursor was when "/" was pressed
	prev   searchPattern // the pattern before it, which esc and an empty query keep
	prevHl bool
}

func newSQLSearch() sqlSearch {
	in := textinput.New()
	in.Prompt = "/"
	return sqlSearch{input: in}
}

// startSQLSearch opens the query line.
func (m *Model) startSQLSearch() tea.Cmd {
	b := &m.sql.buffer
	s := &m.search
	s.typing, s.from, s.prev, s.prevHl = true, b.pos(), b.search, b.hlHits
	s.input.SetValue("")
	return s.input.Focus()
}

// searchSQLFromPalette is the palette's command: it shows the SQL zoomed,
// where "/" searches, and opens the query line.
func (m *Model) searchSQLFromPalette() tea.Cmd {
	if m.cols.shown {
		m.toggleColumns()
	}
	m.setFocus(paneSQL)
	if len(m.sql.text) == 0 {
		m.flash = "no SQL to search"
		return nil
	}
	m.zoomed = true
	return m.startSQLSearch()
}

// handleSearchKey edits the query: enter keeps the search and esc drops it.
func (m *Model) handleSearchKey(msg tea.KeyPressMsg) tea.Cmd {
	b := &m.sql.buffer
	s := &m.search
	switch {
	case key.Matches(msg, m.keys.Escape):
		s.typing = false
		s.input.Blur()
		b.setSearch(s.prev)
		b.hlHits = s.prevHl
		b.restore(s.from)
	case msg.String() == "enter":
		s.typing = false
		s.input.Blur()
		b.restore(s.from)
		if s.input.Value() == "" {
			// As in vim, an empty query repeats the last one.
			b.setSearch(s.prev)
			b.hlHits = s.prevHl
		}
		m.stepSQLSearch(1)
	default:
		before := s.input.Value()
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		if q := s.input.Value(); q != before {
			b.restore(s.from)
			b.setSearch(searchPattern{text: q})
			if len(b.hits) > 0 {
				i, _ := b.nextHit(true)
				b.gotoHit(i)
			}
		}
		return cmd
	}
	return nil
}

// gotoHit puts the cursor at the start of a match.
func (b *buffer) gotoHit(i int) {
	b.row = b.hits[i].row
	b.setCol(b.hits[i].from)
}

// stepSQLSearch is n (d = 1) and N (d = -1): the cursor goes to the next
// match in the search's direction, or against it, around the ends.
func (m *Model) stepSQLSearch(d int) {
	b := &m.sql.buffer
	if b.search.text == "" {
		m.flash = "no search yet: * searches the word under the cursor, / the zoomed sql (z)"
		return
	}
	b.hlHits = true
	if len(b.hits) == 0 {
		m.flash = "not found: " + b.search.text
		return
	}
	forward := (d > 0) != b.search.back
	i, wrapped := b.nextHit(forward)
	b.gotoHit(i)
	switch {
	case wrapped && forward:
		m.flash = "search hit bottom, continuing at top"
	case wrapped:
		m.flash = "search hit top, continuing at bottom"
	}
}

// searchSQLWord is * (and # when back): a search for the word under the
// cursor.
func (m *Model) searchSQLWord(back bool) {
	b := &m.sql.buffer
	word, from := b.wordAtCursor()
	if word == "" {
		m.flash = "no word under the cursor"
		return
	}
	// From the word's start, so # doesn't stop at the word it is on.
	b.col = from
	b.setSearch(searchPattern{text: word, word: true, back: back})
	m.stepSQLSearch(1)
}

// sqlSearchNote is the search's part of the SQL pane's title: the pattern
// and which match the cursor is on.
func (m Model) sqlSearchNote() string {
	b := &m.sql.buffer
	if !b.hlHits || m.cols.shown {
		return ""
	}
	note := "/" + b.search.text
	switch cur := b.curHit(); {
	case len(b.hits) == 0:
		note += " · no match"
	case cur >= 0:
		note += fmt.Sprintf(" · %d of %d", cur+1, len(b.hits))
	case len(b.hits) == 1:
		note += " · 1 match"
	default:
		note += fmt.Sprintf(" · %d matches", len(b.hits))
	}
	return note
}

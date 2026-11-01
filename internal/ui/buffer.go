package ui

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type visualMode int

const (
	visualNone visualMode = iota
	visualChar            // v
	visualLine            // V
)

// buffer is a read-only, vim-like view of highlighted text: a cursor, h/j/k/l
// and word motions, charwise and linewise visual selections that y copies,
// and horizontal scrolling for lines wider than the pane. The embedded
// scroller holds the highlighted lines; text holds the same lines as plain
// runes for motions and yanking.
type buffer struct {
	scroller
	text     [][]rune
	numbered bool // draw a line-number gutter

	row, col int // cursor; col is a rune index into text[row]
	want     int // column j/k aim for (vim's curswant); -1 means end of line
	xoff     int // first text cell shown (horizontal scroll)
	width    int // pane width inside the border, gutter included

	visual     visualMode
	vrow, vcol int  // where the visual selection started
	pendingY   bool // y pressed; a second y yanks the line

	search searchPattern // the last search, which n and N repeat
	hits   []hit         // its matches in text
	hlHits bool          // the matches are highlighted (esc hides them, n shows them)
}

// setText replaces the content. Styled and plain lines must correspond.
func (b *buffer) setText(styled []string, plain [][]rune, numbered bool) {
	b.setLines(styled)
	b.text = plain
	b.numbered = numbered
	b.visual = visualNone
	b.pendingY = false
	b.hits = b.search.find(plain)
	b.row = min(b.row, max(len(plain)-1, 0))
	b.clampCol()
	b.follow()
}

// reset moves the cursor and view to the top left.
func (b *buffer) reset() {
	b.row, b.col, b.want, b.xoff = 0, 0, 0, 0
	b.visual = visualNone
	b.top()
}

func (b *buffer) setWidth(w int) {
	b.width = max(w, 1)
	b.follow()
}

func (b *buffer) gutterWidth() int {
	if !b.numbered {
		return 0
	}
	return len(fmt.Sprint(len(b.text))) + 1
}

func (b *buffer) textWidth() int { return max(b.width-b.gutterWidth(), 1) }

func (b *buffer) line() []rune {
	if b.row < len(b.text) {
		return b.text[b.row]
	}
	return nil
}

// lastCol is the rightmost column the cursor can be on in line l.
func lastCol(l []rune) int { return max(len(l)-1, 0) }

func (b *buffer) clampCol() { b.col = min(max(b.col, 0), lastCol(b.line())) }

// setCol moves horizontally and remembers the column for j/k.
func (b *buffer) setCol(c int) {
	b.col = c
	b.clampCol()
	b.want = b.col
	b.follow()
}

// moveRow moves vertically, aiming for the remembered column.
func (b *buffer) moveRow(n int) {
	b.row = min(max(b.row+n, 0), max(len(b.text)-1, 0))
	b.applyWant()
	b.follow()
}

func (b *buffer) applyWant() {
	if b.want < 0 {
		b.col = lastCol(b.line())
	} else {
		b.col = min(b.want, lastCol(b.line()))
	}
}

func (b *buffer) gotoRow(r int) {
	b.row = min(max(r, 0), max(len(b.text)-1, 0))
	b.setCol(firstNonBlank(b.line()))
}

// halfPage scrolls the view and the cursor together, like ctrl+d/ctrl+u.
func (b *buffer) halfPage(dir int) {
	n := max(b.height/2, 1) * dir
	b.scroll(n)
	b.moveRow(n)
}

// scrollView scrolls without moving the cursor unless it would leave the
// view (ctrl+e/ctrl+y and the mouse wheel).
func (b *buffer) scrollView(n int) {
	b.scroll(n)
	switch {
	case b.row < b.offset:
		b.row = b.offset
	case b.row >= b.offset+b.height:
		b.row = b.offset + b.height - 1
	default:
		return
	}
	b.row = min(b.row, max(len(b.text)-1, 0))
	b.applyWant()
	b.followX()
}

// follow scrolls the view so the cursor is visible.
func (b *buffer) follow() {
	if b.row < b.offset {
		b.offset = b.row
	}
	if b.row >= b.offset+b.height {
		b.offset = b.row - b.height + 1
	}
	b.clamp()
	b.followX()
}

// followX scrolls sideways by half a screen when the cursor leaves the view.
func (b *buffer) followX() {
	l := b.line()
	x := ansi.StringWidth(string(l[:min(b.col, len(l))]))
	w := 1
	if b.col < len(l) {
		w = max(ansi.StringWidth(string(l[b.col])), 1)
	}
	tw := b.textWidth()
	if x < b.xoff || x+w > b.xoff+tw {
		b.xoff = max(x-tw/2, 0)
	}
}

func firstNonBlank(l []rune) int {
	for i, r := range l {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return lastCol(l)
}

// charClass groups runes the way vim's word motions do: blanks, keyword
// characters and punctuation. For WORD motions everything non-blank is one
// class.
func charClass(r rune, big bool) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case big, r == '_', unicode.IsLetter(r), unicode.IsDigit(r):
		return 1
	}
	return 2
}

// wordForward is vim's w (W when big): the start of the next word, crossing
// lines. An empty line counts as a word.
func (b *buffer) wordForward(big bool) {
	r, c := b.row, b.col
	if l := b.text[r]; c < len(l) {
		if cls := charClass(l[c], big); cls != 0 {
			for c < len(l) && charClass(l[c], big) == cls {
				c++
			}
		}
	}
	for {
		l := b.text[r]
		for c < len(l) && charClass(l[c], big) == 0 {
			c++
		}
		if c < len(l) {
			break
		}
		if r == len(b.text)-1 {
			c = lastCol(l)
			break
		}
		r, c = r+1, 0
		if len(b.text[r]) == 0 {
			break
		}
	}
	b.row = r
	b.setCol(c)
}

// wordBackward is vim's b (B when big): the start of the previous word.
func (b *buffer) wordBackward(big bool) {
	r, c := b.row, b.col
	// back steps one position; the end of a line counts as a blank.
	back := func() bool {
		switch {
		case c > 0:
			c--
		case r == 0:
			return false
		default:
			r--
			c = len(b.text[r])
		}
		return true
	}
	if !back() {
		return
	}
	for {
		l := b.text[r]
		if c < len(l) && charClass(l[c], big) != 0 {
			break
		}
		if len(l) == 0 {
			b.row = r
			b.setCol(0)
			return
		}
		if !back() {
			b.row = 0
			b.setCol(0)
			return
		}
	}
	l := b.text[r]
	cls := charClass(l[c], big)
	for c > 0 && charClass(l[c-1], big) == cls {
		c--
	}
	b.row = r
	b.setCol(c)
}

// toggleVisual starts, switches or ends a visual selection.
func (b *buffer) toggleVisual(mode visualMode) {
	switch b.visual {
	case mode:
		b.visual = visualNone
	case visualNone:
		b.visual = mode
		b.vrow, b.vcol = b.row, b.col
	default:
		b.visual = mode
	}
}

// swapVisualEnds is vim's o: jump to the other end of the selection.
func (b *buffer) swapVisualEnds() {
	b.row, b.vrow = b.vrow, b.row
	b.col, b.vcol = b.vcol, b.col
	b.want = b.col
	b.follow()
}

// selection returns the ordered ends of the visual selection.
func (b *buffer) selection() (sr, sc, er, ec int) {
	sr, sc, er, ec = b.vrow, b.vcol, b.row, b.col
	if sr > er || (sr == er && sc > ec) {
		sr, sc, er, ec = er, ec, sr, sc
	}
	return
}

// yankVisual returns the selected text and ends visual mode with the cursor
// at the start of the selection, as vim does.
func (b *buffer) yankVisual() (string, int) {
	sr, sc, er, ec := b.selection()
	var parts []string
	for r := sr; r <= er; r++ {
		l := b.text[r]
		from, to := 0, len(l)
		if b.visual == visualChar {
			if r == sr {
				from = min(sc, len(l))
			}
			if r == er {
				to = min(ec+1, len(l))
			}
		}
		parts = append(parts, string(l[from:max(to, from)]))
	}
	if b.visual == visualLine {
		sc = firstNonBlank(b.text[sr])
	}
	b.visual = visualNone
	b.row = sr
	b.setCol(sc)
	return strings.Join(parts, "\n"), er - sr + 1
}

// yankLine returns the cursor line (yy).
func (b *buffer) yankLine() string { return string(b.line()) }

// posAt maps a cell inside the pane (below the border) to a buffer position.
func (b *buffer) posAt(x, y int) (row, col int) {
	row = min(max(b.offset+y, 0), max(len(b.text)-1, 0))
	cell := b.xoff + max(x-b.gutterWidth(), 0)
	l := b.text[row]
	w := 0
	for i, r := range l {
		w += ansi.StringWidth(string(r))
		if w > cell {
			return row, i
		}
	}
	return row, lastCol(l)
}

// moveTo puts the cursor at a position, e.g. from a mouse click.
func (b *buffer) moveTo(row, col int) {
	b.row = row
	b.setCol(col)
}

// view renders the visible lines: the gutter, the horizontally scrolled
// text, a search's matches, the selection and, when focused, the cursor.
func (b *buffer) view(s styles, focused bool) []string {
	vis := b.visible()
	out := make([]string, len(vis))
	gw, tw := b.gutterWidth(), b.textWidth()
	var sr, sc, er, ec int
	if b.visual != visualNone {
		sr, sc, er, ec = b.selection()
	}
	for i, styled := range vis {
		r := b.offset + i
		plain := b.text[r]
		line := ansi.Cut(styled, b.xoff, b.xoff+tw)
		cellOf := func(c int) int { return ansi.StringWidth(string(plain[:min(c, len(plain))])) - b.xoff }

		if b.hlHits {
			for _, h := range b.rowHits(r) {
				st := s.searchHit
				if focused && r == b.row && h.from == b.col {
					st = s.matchCurrent
				}
				line = paintCells(line, cellOf(h.from), cellOf(h.to), st, tw)
			}
		}
		if b.visual != visualNone && r >= sr && r <= er {
			from, to := 0, len(plain)
			if b.visual == visualChar {
				if r == sr {
					from = sc
				}
				if r == er {
					to = ec + 1
				}
			}
			x0, x1 := cellOf(from), cellOf(to)
			// Show the line break (or an empty line) as selected, like vim.
			if to > len(plain) || (r < er && b.visual == visualChar) || len(plain) == 0 {
				x1++
			}
			line = paintCells(line, x0, x1, s.visual, tw)
		}
		cursor := focused && r == b.row
		x, w := cellOf(b.col), 1
		if b.col < len(plain) {
			w = max(ansi.StringWidth(string(plain[b.col])), 1)
		}
		// Mark text cut off on the right (vim's listchars extends), unless
		// the cursor is on that cell.
		if ansi.StringWidth(string(plain)) > b.xoff+tw && (!cursor || x+w < tw) {
			line = ansi.Cut(line, 0, tw-1) + s.dim.Render("…")
		}
		if cursor {
			line = paintCells(line, x, x+w, s.cursor, tw)
		}
		if gw > 0 {
			num := s.lineNumber
			if focused && r == b.row {
				num = s.curLineNumber
			}
			line = num.Render(fmt.Sprintf("%*d ", gw-1, r+1)) + line
		}
		out[i] = line
	}
	return out
}

// paintCells restyles cells [x0, x1) of a rendered line (clipped to width),
// padding with spaces past the end of the text.
func paintCells(line string, x0, x1 int, st lipgloss.Style, width int) string {
	x0, x1 = max(x0, 0), min(x1, width)
	if x0 >= x1 {
		return line
	}
	lw := ansi.StringWidth(line)
	if lw < x1 {
		line += strings.Repeat(" ", x1-lw)
		lw = x1
	}
	mid := ansi.Strip(ansi.Cut(line, x0, x1))
	return ansi.Cut(line, 0, x0) + st.Render(mid) + ansi.Cut(line, x1, lw)
}

package navigate

import (
	"bytes"
	"regexp"
	"strings"
)

// dbt renders a model's SQL with Jinja: each {{ }} becomes its value, {% %}
// tags and {# #} comments go, and the text between them is kept as it is
// unless an {% if %} drops it or a {% for %} repeats it. So the literal SQL
// between tags is still in the compiled SQL, in order, minus what control
// flow took out, and lining it up traces a dry run's [line:column] back to
// the file.

// endRaw closes a {% raw %} block, whose text Jinja keeps as it is.
var endRaw = regexp.MustCompile(`\{%[-+]?\s*endraw\s*[-+]?%\}`)

// DbtRegion is the literal SQL of a dbt file: the text between its Jinja
// tags and comments. Each piece is trimmed of the whitespace around it,
// which {%- -%} and the tool's whitespace rules may remove, and its End is
// the tag after it.
func DbtRegion(src []byte) Region {
	var r Region
	emit := func(from, to int) {
		text := src[from:to]
		lead := len(text) - len(bytes.TrimLeft(text, " \t\r\n"))
		text = bytes.TrimSpace(text)
		if len(text) > 0 {
			r.Pieces = append(r.Pieces, Piece{Start: from + lead, Text: string(text), End: to})
		}
	}
	lit := 0 // where the current run of literal text starts
	for i := 0; i+1 < len(src); {
		if src[i] != '{' {
			i++
			continue
		}
		var end int // just past the tag
		switch src[i+1] {
		case '{':
			end = tagEnd(src, i+2, "}}") + 2
		case '%':
			end = tagEnd(src, i+2, "%}") + 2
		case '#':
			if e := indexFrom(src, "#}", i+2); e >= 0 {
				end = e + 2
			} else {
				end = len(src)
			}
		default:
			i++
			continue
		}
		end = min(end, len(src))
		emit(lit, i)
		raw := src[i+1] == '%' && end-2 > i+2 && isRaw(src[i+2:end-2])
		lit, i = end, end
		if !raw {
			continue
		}
		if m := endRaw.FindIndex(src[end:]); m != nil {
			emit(end, end+m[0])
			lit, i = end+m[1], end+m[1]
		}
	}
	emit(lit, len(src))
	return r
}

// isRaw reports whether a {% %} tag's text is raw.
func isRaw(body []byte) bool {
	return strings.Trim(string(body), "-+ \t\r\n") == "raw"
}

// AlignSkipping lines up as many of the region's pieces with compiled as it
// can, in order, skipping the ones that aren't there (an {% if %} dropped
// them): of every way to place pieces in order without overlap, it takes
// the one that places the most text, as late as it can (see maxTree).
// CRLFs are compared as LFs, as Align does.
func (r Region) AlignSkipping(compiled string) *SourceMap {
	type node struct {
		piece, start, end, score, prev int
	}
	var nodes []node
	best := newMaxTree(len(compiled)+1, func(i int) int { return nodes[i].score })
	for pi, p := range r.Pieces {
		text := strings.ReplaceAll(p.Text, "\r\n", "\n")
		if text == "" {
			continue
		}
		first := len(nodes)
		for at := 0; at <= len(compiled)-len(text); {
			i := strings.Index(compiled[at:], text)
			if i < 0 {
				break
			}
			start := at + i
			n := node{piece: pi, start: start, end: start + len(text), score: len(text), prev: best.max(start)}
			if n.prev >= 0 {
				n.score += nodes[n.prev].score
			}
			nodes = append(nodes, n)
			at = start + 1
		}
		// After the whole piece, so it never follows itself.
		for j := first; j < len(nodes); j++ {
			best.put(nodes[j].end, j)
		}
	}
	m := &SourceMap{}
	var chain []alignedSpan
	for j := best.max(len(compiled)); j >= 0; j = nodes[j].prev {
		n := nodes[j]
		chain = append(chain, alignedSpan{compStart: n.start, compEnd: n.end, piece: r.Pieces[n.piece]})
	}
	for i := len(chain) - 1; i >= 0; i-- {
		m.spans = append(m.spans, chain[i])
	}
	return m
}

// maxTree is a Fenwick tree over positions 0..n-1 that answers which node
// put at or before a position scores most; of equal scores, the highest
// node (the latest piece, then the latest place). dbt puts the CTEs of the
// ephemeral models a model uses before its own SQL, so a piece found in
// both, a bare "select" say, goes with the model's.
type maxTree struct {
	tree  []int // node index + 1; 0 is none
	score func(node int) int
}

func newMaxTree(n int, score func(int) int) *maxTree {
	return &maxTree{tree: make([]int, n+1), score: score}
}

func (t *maxTree) better(a, b int) bool {
	if a == 0 || b == 0 {
		return b == 0 && a > 0
	}
	sa, sb := t.score(a-1), t.score(b-1)
	return sa > sb || sa == sb && a > b
}

func (t *maxTree) put(pos, node int) {
	for i := pos + 1; i < len(t.tree); i += i & -i {
		if t.better(node+1, t.tree[i]) {
			t.tree[i] = node + 1
		}
	}
}

// max is the best node put at a position up to pos, or -1.
func (t *maxTree) max(pos int) int {
	bestNode := 0
	for i := min(pos+1, len(t.tree)-1); i > 0; i -= i & -i {
		if t.better(t.tree[i], bestNode) {
			bestNode = t.tree[i]
		}
	}
	return bestNode - 1
}

// First is the compiled offset of the first piece that lines up; ok is
// false when none does.
func (m *SourceMap) First() (off int, ok bool) {
	if len(m.spans) == 0 {
		return 0, false
	}
	return m.spans[0].compStart, true
}

// DbtRefTag is the offset of the first {{ }} tag in src whose ref() names
// the model name, or -1.
func DbtRefTag(src []byte, name string) int {
	for _, t := range jinjaTags(src) {
		if !t.Expr {
			continue
		}
		for _, c := range jinjaCalls(src, t.Start, t.End) {
			if r, ok := parseDbtCall(c); ok && r.Source == "" && r.Name == name {
				return t.Start - 2
			}
		}
	}
	return -1
}

package navigate

import (
	"bytes"
	"strings"
)

// Dataform compiles a sqlx file by cutting out its config, js,
// pre_operations and post_operations blocks and substituting every ${...};
// everything else, newlines and SQL comments included, is kept as it is.
// Checked against the fixtures: stg_events.sqlx compiles to "\n\n\n\nWITH…",
// its WITH being on source line 8. pre_operations and post_operations
// bodies compile the same way, each to one statement.
//
// A Region records the literal SQL of one such part, so a position in the
// compiled SQL (a dry run error's [line:column], say) can be traced back to
// the source by lining the literal pieces up with the compiled text.

// Piece is literal SQL: text found at Start in the source. End is where
// the substitution after it starts: Start+len(Text), or for a dbt piece
// (see DbtRegion) the Jinja tag past the whitespace trimmed from it.
type Piece struct {
	Start int
	Text  string
	End   int
}

// Region is SQL Dataform compiles from part of a sqlx file: literal pieces,
// with a substitution or a cut-out block between each two.
type Region struct {
	Pieces []Piece
}

// SQLXRegions are the compiled parts of a sqlx file.
type SQLXRegions struct {
	// Main is the query: the file without its blocks.
	Main Region
	// PreOps and PostOps are the bodies of the pre_operations and
	// post_operations blocks, in order.
	PreOps, PostOps []Region
	// ConfigStart and ConfigEnd bound the config block's body; ConfigStart
	// is -1 when there is none.
	ConfigStart, ConfigEnd int
}

// Regions splits a sqlx file into what Dataform compiles from it.
func Regions(src []byte) SQLXRegions {
	out := SQLXRegions{ConfigStart: -1}
	var main regionBuilder
	for i := 0; i < len(src); {
		if kw, body, ok := sqlxBlockAt(src, i); ok {
			end := matchBrace(src, body)
			switch kw {
			case "config":
				out.ConfigStart, out.ConfigEnd = body, end
			case "pre_operations":
				out.PreOps = append(out.PreOps, scanRegion(src, body, end))
			case "post_operations":
				out.PostOps = append(out.PostOps, scanRegion(src, body, end))
			}
			main.flush()
			i = min(end+1, len(src))
			continue
		}
		i = main.step(src, i, len(src))
	}
	main.flush()
	out.Main = main.region
	return out
}

// sqlxBlockAt reports whether a config, js, pre_operations or
// post_operations block starts at i, and where its body starts.
func sqlxBlockAt(src []byte, i int) (keyword string, body int, ok bool) {
	if i > 0 && isIdentByte(src[i-1]) {
		return "", 0, false
	}
	for _, kw := range []string{"config", "js", "pre_operations", "post_operations"} {
		if !bytes.HasPrefix(src[i:], []byte(kw)) {
			continue
		}
		j := i + len(kw)
		for j < len(src) && isSpace(src[j]) {
			j++
		}
		if j < len(src) && src[j] == '{' {
			return kw, j + 1, true
		}
	}
	return "", 0, false
}

// scanRegion collects the literal pieces of src[start:end].
func scanRegion(src []byte, start, end int) Region {
	var b regionBuilder
	for i := start; i < end; {
		i = b.step(src, i, end)
	}
	b.flush()
	return b.region
}

type regionBuilder struct {
	region Region
	cur    strings.Builder
	start  int
	open   bool
}

// step consumes the source at i (stopping at limit) and returns where to go
// on: a substitution ends the current piece, SQL comments are literal (a
// ${...} in one is left alone), anything else is one literal byte.
func (b *regionBuilder) step(src []byte, i, limit int) int {
	switch {
	case bytes.HasPrefix(src[i:limit], []byte("--")):
		end := min(lineEnd(src, i), limit)
		b.add(src, i, end)
		return end
	case bytes.HasPrefix(src[i:limit], []byte("/*")):
		end := min(commentEnd(src, i), limit)
		b.add(src, i, end)
		return end
	case bytes.HasPrefix(src[i:limit], []byte("${")):
		b.flush()
		return min(matchBrace(src, i+2)+1, limit)
	}
	b.add(src, i, i+1)
	return i + 1
}

func (b *regionBuilder) add(src []byte, from, to int) {
	if !b.open {
		b.start, b.open = from, true
	}
	b.cur.Write(src[from:to])
}

func (b *regionBuilder) flush() {
	if b.open && b.cur.Len() > 0 {
		b.region.Pieces = append(b.region.Pieces, Piece{Start: b.start, Text: b.cur.String(), End: b.start + b.cur.Len()})
	}
	b.cur.Reset()
	b.open = false
}

// SourceMap traces offsets in compiled SQL back to the source.
type SourceMap struct {
	spans []alignedSpan
}

type alignedSpan struct {
	compStart, compEnd int
	piece              Piece
}

// Align lines the region's literal pieces up with compiled, the SQL Dataform
// produced for it, in order. Dataform turns CRLF line endings into LF, so
// pieces are compared that way. Pieces that can't be found (the compiled SQL
// isn't from this source) end the alignment early.
func (r Region) Align(compiled string) *SourceMap {
	m := &SourceMap{}
	pos := 0
	for _, p := range r.Pieces {
		text := strings.ReplaceAll(p.Text, "\r\n", "\n")
		i := strings.Index(compiled[pos:], text)
		if i < 0 {
			break
		}
		start := pos + i
		m.spans = append(m.spans, alignedSpan{compStart: start, compEnd: start + len(text), piece: p})
		pos = start + len(text)
	}
	return m
}

// Source returns the source offset of compiled offset off. An offset inside
// substituted text maps to where the substitution starts in the source (the
// End of the piece before it).
func (m *SourceMap) Source(off int) (int, bool) {
	if len(m.spans) == 0 {
		return 0, false
	}
	prevEnd := m.spans[0].piece.Start
	for _, s := range m.spans {
		if off < s.compStart {
			return prevEnd, true
		}
		if off < s.compEnd {
			return s.piece.Start + sourceIndex(s.piece.Text, off-s.compStart), true
		}
		prevEnd = s.piece.End
	}
	return prevEnd, true
}

// sourceIndex returns the index in text of the byte at index n of text with
// its CRLFs turned into LFs.
func sourceIndex(text string, n int) int {
	i := 0
	for ; i < len(text) && n > 0; i++ {
		if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
			continue
		}
		n--
	}
	return i // a line's LF maps to its CR: the visible end of the line
}

// SQLPart is a stretch of a sqlx file that holds SQL a formatter may
// rewrite, src[Start:End], and the ${ … } substitutions in it as absolute
// [from, to) offsets.
type SQLPart struct {
	Start, End int
	Islands    [][2]int
}

// SQLParts are the parts of a sqlx file that are SQL: each stretch between
// its config, js, pre_operations and post_operations blocks (the main
// query), and the body of each pre_operations and post_operations block.
// The blocks' keywords and braces, the config and js bodies and the
// whitespace between blocks are in no part. ok is false when a block or a
// ${ is never closed, since where it ends is then a guess.
func SQLParts(src []byte) (parts []SQLPart, ok bool) {
	runStart := 0
	run := func(end int) bool {
		if end <= runStart {
			return true
		}
		p, ok := sqlPart(src, runStart, end)
		parts = append(parts, p)
		return ok
	}
	for i := 0; i < len(src); {
		if kw, body, found := sqlxBlockAt(src, i); found {
			end := matchBrace(src, body)
			if end >= len(src) || !run(i) {
				return nil, false
			}
			if kw == "pre_operations" || kw == "post_operations" {
				p, ok := sqlPart(src, body, end)
				if !ok {
					return nil, false
				}
				parts = append(parts, p)
			}
			i, runStart = end+1, end+1
			continue
		}
		i = skipLiteral(src, i, len(src))
	}
	if !run(len(src)) {
		return nil, false
	}
	return parts, true
}

// ConfigBodies are the bodies of a sqlx file's config blocks, as [from, to)
// offsets between their braces, found the way SQLParts finds blocks. ok
// is false when a block or a ${ is never closed.
func ConfigBodies(src []byte) (bodies [][2]int, ok bool) {
	for i := 0; i < len(src); {
		if kw, body, found := sqlxBlockAt(src, i); found {
			end := matchBrace(src, body)
			if end >= len(src) {
				return nil, false
			}
			if kw == "config" {
				bodies = append(bodies, [2]int{body, end})
			}
			i = end + 1
			continue
		}
		if bytes.HasPrefix(src[i:], []byte("${")) && matchBrace(src, i+2) >= len(src) {
			return nil, false
		}
		i = skipLiteral(src, i, len(src))
	}
	return bodies, true
}

// QueryPart is the whole of src as one part, for a query that has no
// blocks (a Scratch query, a plain .sql file).
func QueryPart(src []byte) (SQLPart, bool) { return sqlPart(src, 0, len(src)) }

// sqlPart is src[start:end] with its substitutions found.
func sqlPart(src []byte, start, end int) (SQLPart, bool) {
	p := SQLPart{Start: start, End: end}
	for i := start; i < end; {
		if bytes.HasPrefix(src[i:end], []byte("${")) {
			close := matchBrace(src, i+2)
			if close >= end {
				return p, false
			}
			p.Islands = append(p.Islands, [2]int{i, close + 1})
			i = close + 1
			continue
		}
		i = skipLiteral(src, i, end)
	}
	return p, true
}

// skipLiteral returns where to go on from i (below limit): past a SQL
// comment, which Dataform leaves as it is, ${ … } inside it included, or
// one byte on. A ${ is not skipped here: the caller decides.
func skipLiteral(src []byte, i, limit int) int {
	switch {
	case bytes.HasPrefix(src[i:limit], []byte("--")):
		return min(lineEnd(src, i), limit)
	case bytes.HasPrefix(src[i:limit], []byte("/*")):
		return min(commentEnd(src, i), limit)
	case bytes.HasPrefix(src[i:limit], []byte("${")):
		return min(matchBrace(src, i+2)+1, limit)
	}
	return i + 1
}

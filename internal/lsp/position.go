package lsp

import (
	"bytes"
	"unicode/utf16"
	"unicode/utf8"
)

// byteOffset converts an LSP position in text to a byte offset. With utf16
// the character is counted in UTF-16 code units, otherwise in bytes. Positions
// past the end of a line or the text are clamped.
func byteOffset(text []byte, pos Position, utf16 bool) int {
	start := 0
	for line := 0; line < pos.Line; line++ {
		n := bytes.IndexByte(text[start:], '\n')
		if n < 0 {
			return len(text)
		}
		start += n + 1
	}
	end := len(text)
	if n := bytes.IndexByte(text[start:], '\n'); n >= 0 {
		end = start + n
	}
	if !utf16 {
		return min(start+max(pos.Character, 0), end)
	}
	i, units := start, 0
	for i < end && units < pos.Character {
		r, size := utf8.DecodeRune(text[i:end])
		units += utf16Len(r)
		i += size
	}
	return i
}

// character converts a byte column in line to an LSP character: the column
// itself for utf-8, or the number of UTF-16 code units before it.
func character(line []byte, col int, utf16 bool) int {
	col = min(col, len(line))
	if !utf16 {
		return col
	}
	units := 0
	for i := 0; i < col; {
		r, size := utf8.DecodeRune(line[i:col])
		units += utf16Len(r)
		i += size
	}
	return units
}

func utf16Len(r rune) int {
	if n := utf16.RuneLen(r); n > 0 {
		return n
	}
	return 1 // invalid UTF-8 decodes to RuneError, one unit
}

// lineAt returns the text of the 0-based line n, without its newline.
func lineAt(text []byte, n int) []byte {
	for ; n > 0; n-- {
		i := bytes.IndexByte(text, '\n')
		if i < 0 {
			return nil
		}
		text = text[i+1:]
	}
	if i := bytes.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

// positionAt converts a byte offset in text to an LSP position.
func positionAt(text []byte, offset int, utf16 bool) Position {
	offset = min(max(offset, 0), len(text))
	lineStart := bytes.LastIndexByte(text[:offset], '\n') + 1
	lineEnd := len(text)
	if i := bytes.IndexByte(text[lineStart:], '\n'); i >= 0 {
		lineEnd = lineStart + i
	}
	return Position{
		Line:      bytes.Count(text[:lineStart], []byte("\n")),
		Character: character(text[lineStart:lineEnd], offset-lineStart, utf16),
	}
}

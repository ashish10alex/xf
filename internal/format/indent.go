package format

import (
	"strings"
	"unicode/utf8"
)

func decodeRune(s string) (rune, int) { return utf8.DecodeRuneInString(s) }

// indentLines puts indent before every line but the first, except a line
// that starts inside a string or a comment: its spaces are part of it.
func indentLines(s, indent string) string {
	if indent == "" || !strings.Contains(s, "\n") {
		return s
	}
	inside := startsInside(s)
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" && !inside[i] {
			lines[i] = indent + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// startsInside reports, for each line of SQL, whether it starts inside a
// string literal (quoted, triple-quoted or a backtick name) or a /* */
// comment.
func startsInside(s string) []bool {
	var out []bool
	out = append(out, false)
	quote := "" // the delimiter of the literal or comment we are in
	for i := 0; i < len(s); {
		switch {
		case quote == "":
			switch {
			case strings.HasPrefix(s[i:], "--"), s[i] == '#':
				j := strings.IndexByte(s[i:], '\n')
				if j < 0 {
					i = len(s)
				} else {
					i += j
				}
				continue
			case strings.HasPrefix(s[i:], "/*"):
				quote = "*/"
				i += 2
				continue
			case strings.HasPrefix(s[i:], `'''`), strings.HasPrefix(s[i:], `"""`):
				quote = s[i : i+3]
				i += 3
				continue
			case s[i] == '\'' || s[i] == '"' || s[i] == '`':
				quote = s[i : i+1]
			}
		case strings.HasPrefix(s[i:], quote):
			i += len(quote)
			quote = ""
			continue
		case s[i] == '\\' && quote != "*/" && i+1 < len(s) && s[i+1] != '\n':
			i += 2
			continue
		case s[i] == '\n' && (quote == "'" || quote == `"`):
			// A quoted string ends at the line: the quote was not a string
			// (an apostrophe in a comment-like place); stay out of it.
			quote = ""
		}
		if s[i] == '\n' {
			out = append(out, quote != "")
		}
		i++
	}
	return out
}

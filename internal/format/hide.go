package format

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A ${ … } is not SQL, so sqlfluff can't be given it. Each one is swapped
// for an identifier before a part is formatted, and swapped back after
// (ADR 0024). The identifier is as long as the ${ … } where it fits, so
// the aligned aliases of a SELECT line up as they will once it is back.

// island is one swapped substitution: the original text and what stood
// for it.
type island struct {
	text, name string
	// at is where name starts in the hidden text.
	at int
	// alone says it has its line to itself.
	alone bool
}

// hidden is a part with its ${ … } swapped out.
type hidden struct {
	text    string
	islands []island
	prefix  string
	// anyAlone says an island has its line to itself.
	anyAlone bool
}

// hide swaps the substitutions at spans (offsets into text, [from, to))
// for identifiers, or, with comments, the ones that have a line to
// themselves for a comment: there a clause often stands
// (${when(incremental(), `WHERE …`)}), where no identifier parses.
func hide(text string, spans [][2]int, comments bool) hidden {
	prefix := "_xf"
	for strings.Contains(strings.ToLower(text), prefix) {
		prefix += "x"
	}
	var b strings.Builder
	h := hidden{prefix: prefix}
	last := 0
	for n, s := range spans {
		b.WriteString(text[last:s[0]])
		orig := text[s[0]:s[1]]
		alone := blankBefore(text, s[0]) && blankAfter(text, s[1])
		h.anyAlone = h.anyAlone || alone
		var name string
		if comments && alone {
			name = "-- " + prefix + strconv.Itoa(n) + "_"
		} else {
			width := len(orig)
			if i := strings.IndexByte(orig, '\n'); i >= 0 {
				width = i // a name is one line: the first line's length stands
			}
			name = prefix + strconv.Itoa(n) + "_"
			for len(name) < width {
				name += "_"
			}
		}
		h.islands = append(h.islands, island{text: orig, name: name, at: b.Len(), alone: alone})
		b.WriteString(name)
		last = s[1]
	}
	b.WriteString(text[last:])
	h.text = b.String()
	return h
}

// blankBefore says only spaces come before text[i] on its line.
func blankBefore(text string, i int) bool {
	for i--; i >= 0 && text[i] != '\n'; i-- {
		if text[i] != ' ' && text[i] != '\t' {
			return false
		}
	}
	return true
}

// blankAfter says only spaces follow text[i:] on its line.
func blankAfter(text string, i int) bool {
	for ; i < len(text) && text[i] != '\n'; i++ {
		if text[i] != ' ' && text[i] != '\t' {
			return false
		}
	}
	return true
}

// restore swaps the identifiers in formatted back for the substitutions
// they stood for. A capitalisation rule may have changed an identifier's
// case, so it is found without regard to it. It reports false when one is
// missing, which would lose the substitution.
func (h hidden) restore(formatted string) (string, bool) {
	if len(h.islands) == 0 {
		return formatted, true
	}
	byName := map[string]string{}
	names := make([]string, 0, len(h.islands))
	for _, is := range h.islands {
		byName[strings.ToLower(is.name)] = is.text
		names = append(names, regexp.QuoteMeta(is.name))
	}
	// Longest first: _xf1_ is the start of _xf10_.
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	re := regexp.MustCompile("(?i)" + strings.Join(names, "|"))
	seen := map[string]int{}
	out := re.ReplaceAllStringFunc(formatted, func(m string) string {
		seen[strings.ToLower(m)]++
		return byName[strings.ToLower(m)]
	})
	for _, is := range h.islands {
		if seen[strings.ToLower(is.name)] != 1 {
			return "", false
		}
	}
	return out, true
}

// original maps an offset in the hidden text to the offset in the text it
// was made from; one inside a swapped name maps to where the ${ starts.
func (h hidden) original(off int) int {
	shift := 0
	for _, is := range h.islands {
		if off < is.at {
			break
		}
		if off < is.at+len(is.name) {
			return is.at + shift
		}
		shift += len(is.text) - len(is.name)
	}
	return off + shift
}

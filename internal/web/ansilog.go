package web

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// logSpan is a run of a log line's text in one style: Class names it for
// the page's CSS ("a-red a-b"), "" for plain text.
type logSpan struct {
	Text  string `json:"t"`
	Class string `json:"c,omitempty"`
}

// sgrRE matches a colour code (SGR): ESC [ params m.
var sgrRE = regexp.MustCompile(`\x1b\[([0-9;:]*)m`)

// colorLine is a line of a run's output as the log shows it: its text
// without escape codes and, when it has colour, the spans that colour it.
// A line that redraws itself with \r shows what it ends on, in the colour
// it had by then.
func colorLine(line string) logLine {
	var (
		st    sgrState
		text  strings.Builder
		spans []logSpan
	)
	add := func(s string) {
		if i := strings.LastIndexByte(s, '\r'); i >= 0 {
			s = s[i+1:]
			text.Reset()
			spans = nil
		}
		if s = ansi.Strip(s); s == "" {
			return
		}
		text.WriteString(s)
		c := st.class()
		if n := len(spans); n > 0 && spans[n-1].Class == c {
			spans[n-1].Text += s
			return
		}
		spans = append(spans, logSpan{Text: s, Class: c})
	}
	last := 0
	for _, m := range sgrRE.FindAllStringSubmatchIndex(line, -1) {
		add(line[last:m[0]])
		st.apply(line[m[2]:m[3]])
		last = m[1]
	}
	add(line[last:])
	l := logLine{Text: text.String()}
	for _, sp := range spans {
		if sp.Class != "" {
			l.Spans = spans
			break
		}
	}
	return l
}

// sgrState is the style the colour codes so far have set. Only the eight
// basic colours (and their bright forms) are kept, as the theme's own:
// black and white would vanish on one theme or the other, and 256-colour
// and RGB ones wouldn't follow it, so they show as plain text.
type sgrState struct {
	fg                           string
	bold, dim, italic, underline bool
}

var sgrColors = [8]string{"", "red", "green", "yellow", "blue", "magenta", "cyan", ""}

func (s *sgrState) apply(params string) {
	ps := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	if len(ps) == 0 {
		*s = sgrState{}
		return
	}
	for i := 0; i < len(ps); i++ {
		n, err := strconv.Atoi(ps[i])
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			*s = sgrState{}
		case n == 1:
			s.bold = true
		case n == 2:
			s.dim = true
		case n == 3:
			s.italic = true
		case n == 4:
			s.underline = true
		case n == 22:
			s.bold, s.dim = false, false
		case n == 23:
			s.italic = false
		case n == 24:
			s.underline = false
		case n >= 30 && n <= 37:
			s.fg = sgrColors[n-30]
		case n >= 90 && n <= 97:
			s.fg = sgrColors[n-90]
		case n == 39:
			s.fg = ""
		case n == 38 || n == 48:
			// An extended colour: 5;n or 2;r;g;b follows.
			if n == 38 {
				s.fg = ""
			}
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
}

func (s sgrState) class() string {
	var c []string
	if s.fg != "" {
		c = append(c, "a-"+s.fg)
	}
	if s.bold {
		c = append(c, "a-b")
	}
	if s.dim {
		c = append(c, "a-d")
	}
	if s.italic {
		c = append(c, "a-i")
	}
	if s.underline {
		c = append(c, "a-u")
	}
	return strings.Join(c, " ")
}

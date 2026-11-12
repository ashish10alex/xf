package impact

import (
	"strings"
	"unicode/utf8"
)

// ANSI styles of Text.
const (
	ansiBold   = "1"
	ansiDim    = "2"
	ansiRed    = "31"
	ansiGreen  = "32"
	ansiYellow = "33"
	ansiCyan   = "36"
)

// textMarks are the ledger's marks as Text words them, with their styles.
var textMarks = map[string]struct {
	word   string
	styles []string
}{
	"🚨":  {"FIX", []string{ansiBold, ansiRed}},
	"⚠️": {"REVIEW", []string{ansiBold, ansiYellow}},
	"❔":  {"?", []string{ansiYellow}},
	"ℹ️": {"low", []string{ansiDim}},
	"🔁":  {"renamed", []string{ansiCyan}},
	"🆕":  {"new", []string{ansiCyan}},
	"✅":  {"ok", []string{ansiGreen}},
}

// textPainter wraps text in ANSI styles when it is true.
type textPainter bool

func (color textPainter) paint(s string, styles ...string) string {
	if !color || s == "" {
		return s
	}
	return "\x1b[" + strings.Join(styles, ";") + "m" + s + "\x1b[0m"
}

// plain is a ledger cell without its Markdown: no backticks, bold or
// escaped pipes.
func plain(md string) string {
	return strings.NewReplacer("`", "", "**", "", `\|`, "|").Replace(md)
}

// Text is the report for a terminal, coloured with ANSI escapes when color
// is set: the ledger the Markdown is, a line per model with what it changes
// and, under it, every reader it affects.
func Text(r *Report, color bool) string {
	p := textPainter(color)
	l := BuildLedger(r)
	var b strings.Builder
	b.WriteString(p.paint(plain(l.Title), ansiBold) + "\n")
	// Green only when every model was checked and nothing is to fix.
	summary := []string{ansiBold, ansiGreen}
	for _, row := range l.Rows {
		if row.Mark() == "❔" {
			summary = []string{ansiBold, ansiYellow}
		}
	}
	if l.HasVerdict {
		switch l.Verdict {
		case SeverityCritical:
			summary = []string{ansiBold, ansiRed}
		case SeverityWarning, SeverityUnknown:
			summary = []string{ansiBold, ansiYellow}
		}
	}
	b.WriteString(p.paint(l.Summary, summary...) + " " + p.paint(plain(l.Context), ansiDim) + "\n")
	hint := UnsetHint(r)
	if hint != "" {
		b.WriteString(p.paint("⚠ "+plain(hint), ansiYellow) + "\n")
	}
	if len(l.Rows) == 0 {
		b.WriteString(p.paint("No table, view or incremental table changed.", ansiDim) + "\n")
		return b.String()
	}

	b.WriteString("\n")
	var markW, modelW int
	for _, row := range l.Rows {
		markW = max(markW, utf8.RuneCountInString(textMarks[row.Mark()].word))
		modelW = max(modelW, utf8.RuneCountInString(plain(row.Model)))
	}
	indent := strings.Repeat(" ", markW+2)
	for _, row := range l.Rows {
		m := textMarks[row.Mark()]
		mark := p.paint(m.word, m.styles...) + strings.Repeat(" ", markW-utf8.RuneCountInString(m.word))
		model := plain(row.Model)
		model += strings.Repeat(" ", modelW-utf8.RuneCountInString(model))
		b.WriteString(mark + "  " + p.paint(model, ansiBold) + "  " + plain(row.Changed) + "\n")
		// Every reader under its row: a terminal has the room a pull
		// request's table hasn't, so there is no "Every reader" list.
		for _, a := range row.affectedAll {
			if a == "—" {
				continue // nothing it had is gone, or it wasn't checked
			}
			text, unknown := strings.CutPrefix(plain(a), "❔ ")
			text = "↳ " + text
			switch {
			case strings.HasPrefix(a, "**"):
				text = p.paint(text, ansiBold) // still to fix
			case unknown:
				text = p.paint(text, ansiYellow)
			default:
				text = p.paint(text, ansiDim)
			}
			b.WriteString(indent + text + "\n")
		}
	}

	// The marks a word doesn't explain, then the notes.
	var legend []string
	for _, e := range l.Legend {
		mark, text, _ := strings.Cut(e, " ")
		if m := textMarks[mark]; m.word != "new" && m.word != "renamed" && m.word != "ok" {
			legend = append(legend, m.word+": "+text)
		}
	}
	var foot []string
	if len(legend) > 0 {
		foot = append(foot, strings.Join(legend, " · "))
	}
	for _, note := range l.Notes {
		if note != hint { // shown under the summary
			foot = append(foot, plain(note))
		}
	}
	if len(foot) > 0 {
		b.WriteString("\n")
		for _, line := range foot {
			b.WriteString(p.paint(line, ansiDim) + "\n")
		}
	}
	return b.String()
}

package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/impact"
)

// impactGap is the space between a table's columns.
const impactGap = 2

// impactTable draws rows under a bold header and a rule, no wider than
// width: cells that don't fit wrap. style picks a cell's style; nil leaves
// them all plain.
func (s styles) impactTable(width int, headers []string, rows [][]string, style func(row, col int) lipgloss.Style) []string {
	last := len(headers) - 1
	build := func() *table.Table {
		return table.New().
			Border(lipgloss.NormalBorder()).
			BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
			BorderColumn(false).BorderRow(false).BorderHeader(true).
			BorderStyle(lipgloss.NewStyle().Foreground(s.border)).
			Headers(headers...).
			Rows(rows...).
			StyleFunc(func(row, col int) lipgloss.Style {
				st := s.text
				switch {
				case row == table.HeaderRow:
					st = s.groupHeader
				case style != nil:
					st = style(row, col)
				}
				if col < last {
					st = st.PaddingRight(impactGap)
				}
				return st
			})
	}
	out := build().String()
	if lipgloss.Width(out) > width {
		out = build().Width(width).String()
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

// unsetHintLines is a report's Markdown hint that it used the default
// targets (impact.UnsetHint, cost.UnsetHint), drawn width wide; none for "".
func (s styles) unsetHintLines(hint string, width int) []string {
	if hint == "" {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(ansi.Wrap("⚠ "+strings.ReplaceAll(hint, "`", ""), width, ""), "\n") {
		lines = append(lines, s.warnText.Render(l))
	}
	return lines
}

// ledgerMarks are the rows' marks in the terminal, which draws emoji at
// unreliable widths: a word and its style, by severity or row kind.
func (s styles) ledgerMark(row impact.LedgerRow) (string, lipgloss.Style) {
	if row.Kind == impact.RowAtRisk || row.Kind == impact.RowRenamed && row.Severity != impact.SeverityLow {
		switch row.Severity {
		case impact.SeverityCritical:
			return "FIX", s.errorText.Bold(true)
		case impact.SeverityWarning:
			return "REVIEW", s.warnText.Bold(true)
		case impact.SeverityUnknown:
			return "?", s.warnText
		}
		return "low", s.dim
	}
	switch row.Kind {
	case impact.RowUnchecked:
		return "?", s.warnText
	case impact.RowRenamed:
		return "renamed", s.text
	case impact.RowNew:
		return "new", s.text
	}
	return "ok", s.okText
}

// terminalMarks are the ledger's Markdown marks as the terminal words them,
// for its legend.
var terminalMarks = map[string]string{"🚨": "FIX", "⚠️": "REVIEW", "❔": "?", "ℹ️": "low", "🔁": "renamed", "🆕": "new", "✅": "ok"}

// plainMarkdown is a ledger cell without its Markdown: no backticks or
// bold, and line breaks for <br>.
func plainMarkdown(text string) string {
	text = strings.ReplaceAll(text, "<br>", "\n")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "*", "")
	return strings.ReplaceAll(text, "`", "")
}

// impactReportLines is the report drawn for the terminal, width wide: the
// ledger the Markdown is (impact.BuildLedger), a row per model, then every
// reader.
func (s styles) impactReportLines(r *impact.Report, width int) []string {
	l := impact.BuildLedger(r)
	var lines []string
	lines = append(lines, s.groupHeader.Render(plainMarkdown(l.Title)))
	summary := s.okText
	if l.HasVerdict {
		switch l.Verdict {
		case impact.SeverityCritical:
			summary = s.errorText
		case impact.SeverityWarning, impact.SeverityUnknown:
			summary = s.warnText
		}
	}
	lines = append(lines, wrapIndented(summary.Bold(true).Render(l.Summary)+" "+s.dim.Render(plainMarkdown(l.Context)), width)...)
	lines = append(lines, s.unsetHintLines(impact.UnsetHint(r), width)...)
	lines = append(lines, "")
	if len(l.Rows) == 0 {
		return append(lines, s.dim.Render("No table, view or incremental table changed."))
	}

	rows := make([][]string, len(l.Rows))
	marks := make([]lipgloss.Style, len(l.Rows))
	for i, row := range l.Rows {
		word, st := s.ledgerMark(row)
		marks[i] = st
		affected := make([]string, len(row.Affected))
		for j, a := range row.Affected {
			affected[j] = plainMarkdown(a)
		}
		rows[i] = []string{strconv.Itoa(row.Number), word, plainMarkdown(row.Model), plainMarkdown(row.Changed), strings.Join(affected, "\n")}
	}
	lines = append(lines, s.impactTable(width, []string{"#", "", "Model", "What changed", "Who is affected"}, rows, func(row, col int) lipgloss.Style {
		switch {
		case col == 0:
			return s.dim
		case col == 1 && row >= 0 && row < len(marks):
			return marks[row]
		}
		return s.text
	})...)

	if l.ReadersTotal > 0 {
		var readerRows [][]string
		for _, e := range l.Readers {
			readerRows = append(readerRows, []string{plainMarkdown(e.Reader), plainMarkdown(e.Reads), fmt.Sprintf("%d · %s", e.Row, plainMarkdown(e.Of)), e.Note})
		}
		if l.MoreReaders > 0 {
			readerRows = append(readerRows, []string{fmt.Sprintf("+%d more", l.MoreReaders), "", "", ""})
		}
		lines = append(lines, "", s.text.Render("Every reader ")+s.dim.Render(fmt.Sprintf("(%d)", l.ReadersTotal)), "")
		lines = append(lines, s.impactTable(width, []string{"Reader", "Reads", "Of", "On this branch"}, readerRows, func(_, col int) lipgloss.Style {
			if col == 3 {
				return s.dim
			}
			return s.text
		})...)
	}

	var legend []string
	for _, e := range l.Legend {
		mark, text, _ := strings.Cut(e, " ")
		legend = append(legend, terminalMarks[mark]+": "+text)
	}
	lines = append(lines, "")
	for _, note := range append([]string{strings.Join(legend, " · ")}, l.Notes...) {
		if note == impact.UnsetHint(r) {
			continue // drawn under the summary
		}
		for _, line := range strings.Split(ansi.Wrap(plainMarkdown(note), width, ""), "\n") {
			lines = append(lines, s.dim.Render(line))
		}
	}
	return lines
}

package impact

import (
	"html"
	"regexp"
	"strings"
)

// webRow is a ledger row as xf web's Checks page draws it.
type webRow struct {
	Number       int
	Class, Label string
	Model        string
	Changed      string
	Affected     []webCell
}

// webCell is an entry of "Who is affected": to fix when the reader still
// reads what the row changes.
type webCell struct {
	HTML  string
	ToFix bool
	// None: the cell says nobody is affected.
	None bool
}

type webReader struct {
	Reader, Reads, Of, Note string
	// Row is the number of the ledger row the reader reads.
	Row int
}

// webData is the report as the page lays it out; every string but
// Verdict is HTML.
type webData struct {
	Title, Summary, Context string
	Verdict                 string // fix, review, unknown or ok
	Rows                    []webRow
	Readers                 []webReader
	ReadersTotal            int
	MoreReaders             int
	Legend                  []webLegend
	Notes                   []string
}

type webLegend struct {
	Class, Label, Text string
}

var (
	inlineCode   = regexp.MustCompile("`([^`]+)`")
	inlineStrong = regexp.MustCompile(`\*\*(.+?)\*\*`)
	inlineEm     = regexp.MustCompile(`\*([^*]+)\*`)
)

// inlineHTML is a ledger cell's Markdown as HTML: its text escaped, then
// `code`, **bold** and *italic*, the only Markdown the ledger writes.
func inlineHTML(md string) string {
	s := html.EscapeString(strings.ReplaceAll(md, `\|`, "|"))
	s = inlineCode.ReplaceAllStringFunc(s, func(code string) string {
		// Long names wrap at their underscores and dots, not mid-word.
		name := strings.Trim(code, "`")
		name = strings.NewReplacer("_", "_<wbr>", ".", ".<wbr>").Replace(name)
		return "<code>" + name + "</code>"
	})
	s = inlineStrong.ReplaceAllString(s, "<strong>$1</strong>")
	s = inlineEm.ReplaceAllString(s, "<em>$1</em>")
	return s
}

// rowStyle is a row's class and label on the page, as the terminal words
// its mark.
func rowStyle(r LedgerRow) (class, label string) {
	if r.Kind == RowAtRisk || r.Kind == RowRenamed && r.Severity != SeverityLow {
		switch r.Severity {
		case SeverityCritical:
			return "fix", "Fix"
		case SeverityWarning:
			return "review", "Review"
		case SeverityUnknown:
			return "unknown", "Unknown"
		}
		return "low", "Low"
	}
	switch r.Kind {
	case RowUnchecked:
		return "unknown", "Not checked"
	case RowRenamed:
		return "renamed", "Renamed"
	case RowNew:
		return "new", "New"
	}
	return "ok", "OK"
}

// markStyles are the legend's marks as the page labels them.
var markStyles = map[string][2]string{
	"🚨": {"fix", "Fix"}, "⚠️": {"review", "Review"}, "❔": {"unknown", "Unknown"}, "ℹ️": {"low", "Low"},
	"🔁": {"renamed", "Renamed"}, "🆕": {"new", "New"}, "✅": {"ok", "OK"},
}

// PageData is the report as the page lays it out, its cells as HTML, for
// xf web's I overlay to draw.
func PageData(r *Report) any { return pageData(r) }

func pageData(r *Report) webData {
	l := BuildLedger(r)
	d := webData{
		Title:        inlineHTML(l.Title),
		Summary:      inlineHTML(l.Summary),
		Context:      inlineHTML(l.Context),
		Verdict:      "ok",
		ReadersTotal: l.ReadersTotal,
		MoreReaders:  l.MoreReaders,
	}
	if l.HasVerdict {
		d.Verdict = map[Severity]string{SeverityCritical: "fix", SeverityWarning: "review", SeverityUnknown: "unknown", SeverityLow: "ok"}[l.Verdict]
	}
	for _, row := range l.Rows {
		class, label := rowStyle(row)
		w := webRow{Number: row.Number, Class: class, Label: label, Model: inlineHTML(row.Model), Changed: inlineHTML(row.Changed)}
		for _, a := range row.Affected {
			toFix := strings.HasPrefix(a, "**") && strings.HasSuffix(a, "**")
			w.Affected = append(w.Affected, webCell{HTML: inlineHTML(strings.TrimSuffix(strings.TrimPrefix(a, "**"), "**")), ToFix: toFix,
				None: a == "—" || a == "nobody"})
		}
		d.Rows = append(d.Rows, w)
	}
	for _, e := range l.Readers {
		d.Readers = append(d.Readers, webReader{Reader: inlineHTML(e.Reader), Reads: inlineHTML(e.Reads), Of: inlineHTML(e.Of), Note: inlineHTML(e.Note), Row: e.Row})
	}
	for _, e := range l.Legend {
		mark, text, _ := strings.Cut(e, " ")
		st := markStyles[mark]
		d.Legend = append(d.Legend, webLegend{Class: st[0], Label: st[1], Text: text})
	}
	for _, n := range l.Notes {
		d.Notes = append(d.Notes, inlineHTML(n))
	}
	return d
}

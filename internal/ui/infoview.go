package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gcpauth"
)

// infoOverlay is the , overlay's state. The tool's version, the git state
// and the Google accounts are read when it opens; until they arrive it shows the last ones,
// or "…". "/" searches it as vim does: every match is highlighted, the
// view goes to the first as the query is typed, and n and N go from one
// matching row to the next.
type infoOverlay struct {
	open   bool
	offset int  // first visible line
	zoomed bool // the box fills the body (z)
	seq    int  // counts the opens, so an older open's answers are dropped

	input  textinput.Model
	typing bool   // "/" was pressed: keys edit the query until enter or esc
	from   int    // the offset when "/" was pressed, which esc goes back to
	cur    infoAt // the current match

	version     string
	versionErr  error
	versionDone bool
	git         InfoGit
	gitErr      error
	gitDone     bool
	adc         gcpauth.ADC
	adcDone     bool
	gcloud      gcpauth.CLI
	gcloudDone  bool
}

func newInfoOverlay() infoOverlay {
	in := textinput.New()
	in.Prompt = "/"
	return infoOverlay{input: in}
}

func (o *infoOverlay) query() string { return strings.TrimSpace(o.input.Value()) }

// clearSearch drops the query and its highlights.
func (o *infoOverlay) clearSearch() {
	o.typing, o.cur = false, infoAt{}
	o.input.SetValue("")
	o.input.Blur()
}

type infoVersionMsg struct {
	seq     int
	version string
	err     error
}

type infoGitMsg struct {
	seq int
	git InfoGit
	err error
}

type infoADCMsg struct {
	seq int
	adc gcpauth.ADC
}

type infoGcloudMsg struct {
	seq    int
	gcloud gcpauth.CLI
}

// openInfo shows the project info from the top, at the normal size, and
// reads what may have changed since it was last open.
func (m *Model) openInfo() tea.Cmd {
	info := m.deps.Info
	if info == nil {
		return nil
	}
	o := &m.info
	o.open, o.offset, o.zoomed = true, 0, false
	o.clearSearch()
	o.seq++
	seq := o.seq
	var cmds []tea.Cmd
	if f := info.ToolVersion; f != nil {
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), infoTimeout)
			defer cancel()
			v, err := f(ctx)
			return infoVersionMsg{seq: seq, version: v, err: err}
		})
	}
	if f := info.Git; f != nil {
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), infoTimeout)
			defer cancel()
			g, err := f(ctx)
			return infoGitMsg{seq: seq, git: g, err: err}
		})
	}
	if f := info.ADC; f != nil {
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), infoTimeout)
			defer cancel()
			return infoADCMsg{seq: seq, adc: f(ctx)}
		})
	}
	if f := info.Gcloud; f != nil {
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), infoTimeout)
			defer cancel()
			return infoGcloudMsg{seq: seq, gcloud: f(ctx)}
		})
	}
	return tea.Batch(cmds...)
}

// accountHint points at the , overlay from an error that the account xf
// signs in to Google with may explain; "" when the overlay doesn't say it.
func (m Model) accountHint() string {
	if m.deps.Info == nil || m.deps.Info.ADC == nil {
		return ""
	}
	return "press , to see the account xf uses"
}

// handleInfoKey handles a key while the , overlay is open. Esc leaves one
// state at a time: the search, then the zoom, then the overlay.
func (m *Model) handleInfoKey(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	o := &m.info
	if o.typing {
		switch {
		case key.Matches(msg, k.Escape):
			o.clearSearch()
			o.offset = o.from
		case msg.String() == "enter":
			o.typing = false
			o.input.Blur()
			if o.query() == "" {
				o.clearSearch()
			}
		default:
			before := o.query()
			var cmd tea.Cmd
			o.input, cmd = o.input.Update(msg)
			if o.query() != before {
				m.seekInfo()
			}
			return cmd
		}
		return nil
	}
	page := max(m.infoLayout(m.geometry().bodyH).rows/2, 1)
	switch {
	case key.Matches(msg, k.Escape) && o.query() != "":
		o.clearSearch()
	case key.Matches(msg, k.Escape) && o.zoomed:
		o.zoomed = false
	case key.Matches(msg, k.Close, k.Info):
		o.open = false
	case key.Matches(msg, k.Find):
		o.clearSearch()
		o.typing, o.from = true, m.infoLayout(m.geometry().bodyH).clamp(o.offset)
		return o.input.Focus()
	case key.Matches(msg, k.NextMatch):
		m.stepInfo(1)
	case key.Matches(msg, k.PrevMatch):
		m.stepInfo(-1)
	case key.Matches(msg, k.CopyReport):
		m.flash = "copied the project info"
		return m.copyText(ansi.Strip(strings.Join(m.infoLines(0), "\n")) + "\n")
	case key.Matches(msg, k.Zoom):
		o.zoomed = !o.zoomed
	case key.Matches(msg, k.Up):
		m.scrollInfo(-1)
	case key.Matches(msg, k.Down):
		m.scrollInfo(1)
	case key.Matches(msg, k.HalfUp):
		m.scrollInfo(-page)
	case key.Matches(msg, k.HalfDown):
		m.scrollInfo(page)
	case key.Matches(msg, k.Top):
		o.offset = 0
	case key.Matches(msg, k.Bottom):
		m.scrollInfo(len(m.infoLayout(m.geometry().bodyH).list))
	}
	return nil
}

// scrollInfo moves the project info by delta lines, within it.
func (m *Model) scrollInfo(delta int) {
	lay := m.infoLayout(m.geometry().bodyH)
	m.info.offset = lay.clamp(m.info.offset + delta)
}

// seekInfo makes the first match at or after where the search started the
// current one and shows it; with no match the view stays where it started.
func (m *Model) seekInfo() {
	o := &m.info
	o.offset, o.cur = o.from, infoAt{}
	entries := m.infoEntries()
	matches, _ := m.infoMatches(entries)
	if len(matches) == 0 {
		return
	}
	_, spans := m.infoRender(m.infoLayout(m.geometry().bodyH).inner)
	first := matches[0]
	for _, i := range matches {
		if spans[i][0] >= o.from {
			first = i
			break
		}
	}
	o.cur = entries[first].at
	m.revealInfo(first)
}

// stepInfo makes the match d matches on the current one, around the ends,
// and shows it.
func (m *Model) stepInfo(d int) {
	entries := m.infoEntries()
	matches, cur := m.infoMatches(entries)
	if len(matches) == 0 {
		return
	}
	next := matches[(cur+d+len(matches))%len(matches)]
	m.info.cur = entries[next].at
	m.revealInfo(next)
}

// infoContext is how many lines stay visible past a match the view
// scrolled to.
const infoContext = 2

// revealInfo scrolls the i-th entry into view, if it isn't in it.
func (m *Model) revealInfo(i int) {
	lay := m.infoLayout(m.geometry().bodyH)
	_, spans := m.infoRender(lay.inner)
	first, end := spans[i][0], spans[i][1]
	off := lay.clamp(m.info.offset)
	switch {
	case first < off:
		off = first - infoContext
	case end > off+lay.rows:
		off = min(end+infoContext-lay.rows, first)
	}
	m.info.offset = lay.clamp(off)
}

// infoLayout lays the , overlay out in bodyH lines.
func (m Model) infoLayout(bodyH int) errorsBoxLayout {
	keys := [][2]string{{"esc", "close"}, {"/", "search"}, {"y", "copy"}}
	if m.info.query() != "" {
		keys = [][2]string{{"esc", "clear search"}, {"n/N", "next"}, {"y", "copy"}}
	}
	return m.scrollBoxLayout(bodyH, m.info.zoomed, m.infoLines, keys...)
}

// infoBox draws the , overlay, its title saying what is searched for and
// which of the matching rows is the current one.
func (m Model) infoBox(g geometry) string {
	title := "project info"
	if q := m.info.query(); q != "" {
		found := "no match"
		if matches, cur := m.infoMatches(m.infoEntries()); len(matches) > 0 {
			found = fmt.Sprintf("%d of %d", cur+1, len(matches))
		}
		title += " · /" + q + " · " + found
	}
	return m.scrollBox(title, m.infoLayout(g.bodyH), m.info.offset, m.info.zoomed)
}

// infoFooter is the footer's keys while the , overlay is open.
func (m Model) infoFooter() []key.Binding {
	k := m.keys
	zoom := k.Zoom
	zoom.SetHelp("z", "zoom")
	if m.info.zoomed {
		zoom.SetHelp("z", "unzoom")
	}
	copyAll := k.CopyReport
	copyAll.SetHelp("y", "copy")
	search := k.Find
	search.SetHelp("/", "search")
	if m.info.query() == "" {
		return []key.Binding{k.Close, search, copyAll, zoom}
	}
	done := k.Close
	done.SetHelp("esc", "clear search")
	return []key.Binding{done, k.NextMatch, search, copyAll, zoom}
}

// infoAt names an entry of the , overlay by its section and its place in
// it, which the values that arrive late don't change; row 0 is the
// section's title.
type infoAt struct {
	section string
	row     int
}

// infoEntry is a section's title or a row of the , overlay, before it is
// laid out: a label padded to labelW cells, a value padded to valueW and a
// dim note.
type infoEntry struct {
	at                 infoAt
	title              string
	label, value, note string
	labelW, valueW     int
	warn               bool // the value is a warning
}

// matches reports whether q (lower case) is in one of the entry's texts.
func (e infoEntry) matches(q string) bool {
	return hitMask(e.title, q) != nil || hitMask(e.label, q) != nil || hitMask(e.value, q) != nil || hitMask(e.note, q) != nil
}

// infoMatches are the entries the query matches and which of them is the
// current match: the one at info.cur, or the next when that one doesn't
// match, as when a value arrived late. Without a match cur is -1.
func (m Model) infoMatches(entries []infoEntry) (matches []int, cur int) {
	q := strings.ToLower(m.info.query())
	if q == "" {
		return nil, -1
	}
	at := 0
	for i, e := range entries {
		if e.at == m.info.cur {
			at = i
		}
		if e.matches(q) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return nil, -1
	}
	for j, i := range matches {
		if i >= at {
			return matches, j
		}
	}
	return matches, 0
}

// infoLines are the , overlay's contents, with values too long for w cells
// wrapped under their column; 0 wraps nothing, for copying.
func (m Model) infoLines(w int) []string {
	lines, _ := m.infoRender(w)
	return lines
}

// infoRender lays the entries out in w cells, the query's matches
// highlighted, and says which lines each entry is on: its first and the
// one past its last.
func (m Model) infoRender(w int) (lines []string, spans [][2]int) {
	s := m.styles
	entries := m.infoEntries()
	q := strings.ToLower(m.info.query())
	matches, cur := m.infoMatches(entries)
	for i, e := range entries {
		hit := s.match
		if cur >= 0 && matches[cur] == i {
			hit = s.matchCurrent
		}
		if e.title != "" && len(lines) > 0 {
			lines = append(lines, "")
		}
		first := len(lines)
		if e.title != "" {
			lines = append(lines, paint(e.title, hitMask(e.title, q), s.title.Render, hit))
		} else {
			lines = append(lines, infoRow(s, e, q, hit, w)...)
		}
		spans = append(spans, [2]int{first, len(lines)})
	}
	return lines, spans
}

// infoEntries are the , overlay's sections and their rows (see
// frontend.InfoEntries).
func (m Model) infoEntries() []infoEntry {
	o := m.info
	read := frontend.InfoRead{
		Version: o.version, VersionErr: o.versionErr, VersionDone: o.versionDone,
		Git: o.git, GitErr: o.gitErr, GitDone: o.gitDone,
		ADC: o.adc, ADCDone: o.adcDone, Gcloud: o.gcloud, GcloudDone: o.gcloudDone,
		LastEditor: m.lastEditor, Following: m.following,
	}
	var entries []infoEntry
	for _, e := range frontend.InfoEntries(m.deps, read) {
		entries = append(entries, infoEntry{at: infoAt{section: e.Section, row: e.Row}, title: e.Title,
			label: e.Label, value: e.Value, note: e.Note, labelW: e.LabelW, valueW: e.ValueW, warn: e.Warn})
	}
	return entries
}

// infoRow is a row of the , overlay: a label, a value padded to its width
// and a dim note, wrapped to w cells (0 wraps nothing), with what q (lower
// case) matches in hit. A value and note that don't fit one line, even
// with the value unpadded, go on lines of their own, wrapped, all under
// the value's column.
func infoRow(s styles, e infoEntry, q string, hit lipgloss.Style, w int) []string {
	plain := func(strs ...string) string { return strings.Join(strs, "") }
	value, note, valueW := e.value, e.note, e.valueW
	valueStyle := plain
	if e.warn {
		valueStyle = s.warnText.Render
	}
	valueMask, noteMask := hitMask(value, q), hitMask(note, q)

	lead := "  "
	if e.labelW > 0 {
		lead += pad(e.label, e.labelW) + "  "
	}
	indent := strings.Repeat(" ", ansi.StringWidth(lead))
	prefix := s.dim.Render(lead)
	if mask := hitMask(e.label, q); mask != nil {
		prefix = s.dim.Render("  ") + paint(e.label, mask, s.dim.Render, hit) + s.dim.Render(lead[2+len(e.label):])
	}
	if note == "" {
		valueW = 0
	}
	line := func(valueW int) string {
		l := prefix + paint(value, valueMask, valueStyle, hit) + pad("", valueW-ansi.StringWidth(value))
		if note != "" {
			l += "  " + paint(note, noteMask, s.dim.Render, hit)
		}
		return strings.TrimRight(l, " ")
	}
	avail := w - len(indent)
	if one := line(valueW); w <= 0 || ansi.StringWidth(one) <= w || avail < 10 {
		return []string{one}
	}
	// Out of its column, the note may still fit beside the value.
	if one := line(0); ansi.StringWidth(one) <= w {
		return []string{one}
	}
	var out []string
	for _, part := range []struct {
		text  string
		mask  []bool
		style func(...string) string
	}{{value, valueMask, valueStyle}, {note, noteMask, s.dim.Render}} {
		if part.text == "" {
			continue
		}
		wrapped, masks := wrapMasked(part.text, part.mask, avail)
		for i, l := range wrapped {
			l = paint(l, masks[i], part.style, hit)
			if len(out) == 0 {
				out = append(out, prefix+l)
			} else {
				out = append(out, indent+l)
			}
		}
	}
	return out
}

// hitMask marks the bytes of text that are in a case-insensitive
// occurrence of q (lower case); nil when there is none.
func hitMask(text, q string) []bool {
	lower := strings.ToLower(text)
	if q == "" || len(lower) != len(text) { // byte offsets must line up
		return nil
	}
	var mask []bool
	for from := 0; ; {
		i := strings.Index(lower[from:], q)
		if i < 0 {
			return mask
		}
		if mask == nil {
			mask = make([]bool, len(text))
		}
		for j := from + i; j < from+i+len(q); j++ {
			mask[j] = true
		}
		from += i + len(q)
	}
}

// paint renders text in base, and its bytes that mask marks in hit.
func paint(text string, mask []bool, base func(...string) string, hit lipgloss.Style) string {
	if mask == nil {
		return base(text)
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && mask[j] == mask[i] {
			j++
		}
		if mask[i] {
			b.WriteString(hit.Render(text[i:j]))
		} else {
			b.WriteString(base(text[i:j]))
		}
		i = j
	}
	return b.String()
}

// wrapMasked wraps text to w cells and gives each line the marks of its
// bytes, so a match the wrap cut in two is marked on both lines.
func wrapMasked(text string, mask []bool, w int) (lines []string, masks [][]bool) {
	lines = strings.Split(ansi.Wrap(text, w, "/._,-"), "\n")
	masks = make([][]bool, len(lines))
	if mask == nil {
		return lines, masks
	}
	at := 0
	for i, l := range lines {
		masks[i] = make([]bool, len(l))
		for j := 0; j < len(l); j++ {
			// The wrap drops the spaces it breaks at.
			for at < len(text) && text[at] != l[j] {
				at++
			}
			if at < len(text) {
				masks[i][j] = mask[at]
				at++
			}
		}
	}
	return lines, masks
}

// pad pads s with spaces to w cells.
func pad(s string, w int) string {
	return s + strings.Repeat(" ", max(w-ansi.StringWidth(s), 0))
}

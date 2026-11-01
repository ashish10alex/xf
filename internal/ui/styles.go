package ui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"github.com/ashish10alex/xf/internal/config"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// styles holds every style the UI uses. It is rebuilt when the terminal
// reports its background colour, so light and dark terminals both read well.
type styles struct {
	isDark bool

	border, focusBorder color.Color
	title, focusTitle   lipgloss.Style
	// accent marks what runs in GCP: the panes that run, while the a
	// toggle is on.
	accent      color.Color
	accentTitle lipgloss.Style

	dim, text, selected, selectedDim lipgloss.Style
	groupHeader                      lipgloss.Style
	kindColors                       map[string]lipgloss.Style
	lineNumber, curLineNumber        lipgloss.Style
	cursor, visual                   lipgloss.Style // SQL pane cursor and selection
	errorText, okText, warnText      lipgloss.Style

	badge, statusBar      lipgloss.Style
	footerKey, footerDesc lipgloss.Style
	match                 lipgloss.Style // palette characters the query matched
	matchCurrent          lipgloss.Style // the current match of a search
	searchHit             lipgloss.Style // the other matches of the SQL pane's search
	selBg                 color.Color
	ramp                  []color.Color // looping gradient for the compile animation

	chromaStyle string
}

// newStyles is the light or dark palette with its own text colour, for a
// theme the user picked.
func newStyles(isDark bool) styles { return buildStyles(isDark, false) }

// themeStyles is the palette theme shows on a terminal whose background is
// dark or not. Auto draws plain text in the terminal's own foreground, so it
// reads on any background: a fixed near-black or near-white is lost when the
// terminal's palette or the background reported differs from what xf thinks.
func themeStyles(theme string, termDark bool) styles {
	return buildStyles(themeDark(theme, termDark), theme == config.ThemeAuto)
}

// buildStyles is the light or dark palette; termFg leaves plain text in the
// terminal's foreground.
func buildStyles(isDark, termFg bool) styles {
	ld := lipgloss.LightDark(isDark)
	green := ld(lipgloss.Color("#1a7f37"), lipgloss.Color("#8fd694"))
	muted := ld(lipgloss.Color("#6e7781"), lipgloss.Color("#7d8590"))
	fg := ld(lipgloss.Color("#1f2328"), lipgloss.Color("#e6edf3"))
	if termFg {
		fg = lipgloss.NoColor{}
	}
	selBg := ld(lipgloss.Color("#dbe9f9"), lipgloss.Color("#2d3440"))
	red := ld(lipgloss.Color("#cf222e"), lipgloss.Color("#ff7b72"))
	amber := ld(lipgloss.Color("#9a6700"), lipgloss.Color("#e3b341"))

	s := styles{
		isDark:      isDark,
		border:      ld(lipgloss.Color("#d0d7de"), lipgloss.Color("#3d444d")),
		focusBorder: green,
		title:       lipgloss.NewStyle().Foreground(muted),
		focusTitle:  lipgloss.NewStyle().Foreground(green).Bold(true),
		dim:         lipgloss.NewStyle().Foreground(muted),
		text:        lipgloss.NewStyle().Foreground(fg),
		selected:    lipgloss.NewStyle().Foreground(fg).Background(selBg).Bold(true),
		selectedDim: lipgloss.NewStyle().Foreground(muted).Background(selBg),
		groupHeader: lipgloss.NewStyle().Foreground(fg).Bold(true),
		kindColors: map[string]lipgloss.Style{
			"tbl":  lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#0969da"), lipgloss.Color("#79c0ff"))),
			"view": lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#1a7f37"), lipgloss.Color("#7ee787"))),
			"inc":  lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#8250df"), lipgloss.Color("#d2a8ff"))),
			"op":   lipgloss.NewStyle().Foreground(amber),
			"asrt": lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#bf3989"), lipgloss.Color("#ff9bce"))),
			"test": lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#bf3989"), lipgloss.Color("#ff9bce"))),
			"mv":   lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#1a7f37"), lipgloss.Color("#7ee787"))),
			"seed": lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#0969da"), lipgloss.Color("#79c0ff"))),
			"snap": lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#8250df"), lipgloss.Color("#d2a8ff"))),
		},
		lineNumber:    lipgloss.NewStyle().Foreground(muted),
		curLineNumber: lipgloss.NewStyle().Foreground(fg).Bold(true),
		cursor:        lipgloss.NewStyle().Reverse(true),
		visual:        lipgloss.NewStyle().Foreground(fg).Background(ld(lipgloss.Color("#b6d7ff"), lipgloss.Color("#264f78"))),
		errorText:     lipgloss.NewStyle().Foreground(red),
		okText:        lipgloss.NewStyle().Foreground(green),
		warnText:      lipgloss.NewStyle().Foreground(amber),
		badge:         lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#ffffff"), lipgloss.Color("#0d1117"))).Background(green).Bold(true).Padding(0, 1),
		statusBar:     lipgloss.NewStyle().Foreground(muted),
		footerKey:     lipgloss.NewStyle().Foreground(fg).Bold(true),
		footerDesc:    lipgloss.NewStyle().Foreground(muted),
		match:         lipgloss.NewStyle().Foreground(green).Bold(true),
		matchCurrent:  lipgloss.NewStyle().Foreground(ld(lipgloss.Color("#ffffff"), lipgloss.Color("#0d1117"))).Background(amber).Bold(true),
		searchHit:     lipgloss.NewStyle().Foreground(fg).Background(ld(lipgloss.Color("#fff2b3"), lipgloss.Color("#5c4a00"))),
		selBg:         selBg,
		chromaStyle:   "github",
	}
	blue := ld(lipgloss.Color("#0969da"), lipgloss.Color("#79c0ff"))
	s.accent, s.accentTitle = blue, lipgloss.NewStyle().Foreground(blue)
	s.ramp = lipgloss.Blend1D(40,
		blue,
		ld(lipgloss.Color("#8250df"), lipgloss.Color("#d2a8ff")),
		ld(lipgloss.Color("#bf3989"), lipgloss.Color("#ff9bce")),
		amber,
		green,
		blue,
	)
	if isDark {
		s.chromaStyle = "github-dark"
	}
	return s
}

// kind styles the short kind label of an action.
func (s styles) kind(short string) lipgloss.Style {
	if st, ok := s.kindColors[short]; ok {
		return st
	}
	return s.dim
}

// pane draws a box of exactly w×h cells with the title set into the top
// border, lazygit style: ╭─1 actions──────╮. Body lines are truncated to fit
// and padded with blank lines.
func (s styles) pane(title, body string, w, h int, focused bool) string {
	return s.paneWithFooter(title, "", body, w, h, focused)
}

// paneWithFooter is pane with a note set into the bottom border, right
// aligned: ╰────── ↓ 9 more ─╯.
func (s styles) paneWithFooter(title, footer, body string, w, h int, focused bool) string {
	return s.apiPane(title, footer, body, w, h, focused, false)
}

// apiPane is paneWithFooter for a pane whose runs go to GCP while api is
// set: its title ends " · GCP" in the accent and, unless the focus colours
// it, so does its border.
func (s styles) apiPane(title, footer, body string, w, h int, focused, api bool) string {
	if w < 2 || h < 2 {
		return ""
	}
	inner := w - 2
	bc, ts := s.border, s.title
	switch {
	case focused:
		bc, ts = s.focusBorder, s.focusTitle
	case api:
		bc, ts = s.accent, s.accentTitle
	}
	bs := lipgloss.NewStyle().Foreground(bc)

	var suffix string
	if api {
		suffix = " · GCP"
	}
	room := max(inner-2, 0)
	t := ansi.Truncate(title, max(room-ansi.StringWidth(suffix), 0), "…")
	tw := ansi.StringWidth(t + suffix)
	top := bs.Render("╭─") + ts.Render(t) + s.accentTitle.Bold(focused).Render(suffix) + bs.Render(strings.Repeat("─", max(inner-1-tw, 0))+"╮")

	lines := fitLines(body, inner, h-2)
	var b strings.Builder
	b.WriteString(top)
	for _, l := range lines {
		b.WriteString("\n" + bs.Render("│") + l + bs.Render("│"))
	}
	bottom := bs.Render("╰" + strings.Repeat("─", inner) + "╯")
	if footer != "" && ansi.StringWidth(footer)+4 <= inner {
		note := " " + footer + " "
		bottom = bs.Render("╰"+strings.Repeat("─", inner-ansi.StringWidth(note)-1)) + ts.Render(note) + bs.Render("─╯")
	}
	b.WriteString("\n" + bottom)
	return b.String()
}

// fitLines splits body into exactly h lines of exactly w cells.
func fitLines(body string, w, h int) []string {
	src := strings.Split(body, "\n")
	out := make([]string, h)
	for i := range out {
		var l string
		if i < len(src) {
			l = ansi.Truncate(src[i], w, "…")
		}
		out[i] = l + strings.Repeat(" ", max(w-ansi.StringWidth(l), 0))
	}
	return out
}

// validTheme is theme if it names one (auto, light or dark), else auto.
func validTheme(theme string) string {
	switch theme {
	case config.ThemeLight, config.ThemeDark:
		return theme
	}
	return config.ThemeAuto
}

// themeDark reports whether theme is dark: light and dark say so, and auto
// follows the terminal's background, termDark.
func themeDark(theme string, termDark bool) bool {
	switch theme {
	case config.ThemeLight:
		return false
	case config.ThemeDark:
		return true
	}
	return termDark
}

// restyle rebuilds the styles for the theme and the terminal's background.
func (m *Model) restyle() {
	m.styles = themeStyles(m.theme, m.termDark)
	m.help.Styles = help.DefaultStyles(m.styles.isDark)
	m.refreshSelection()
	m.drawImpact()
	m.drawCost()
}

// cycleTheme shows the next theme (auto, light, dark), saves it and tells
// the engine, so the other TUIs follow.
func (m *Model) cycleTheme() {
	m.theme = config.NextTheme(m.theme)
	m.restyle()
	m.deps.Engine.SetTheme(m.theme)
	m.flash = "theme: " + m.theme
	if m.deps.SaveTheme != nil {
		if err := m.deps.SaveTheme(m.theme); err != nil {
			m.flash = "theme: " + m.theme + " (not saved: " + err.Error() + ")"
		}
	}
}

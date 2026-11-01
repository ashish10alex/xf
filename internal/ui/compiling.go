package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// animTickMsg advances the compile animation by one frame.
type animTickMsg struct{}

const animInterval = 80 * time.Millisecond

// animate schedules the next animation frame. Tests replace it so drive()
// never waits on the timer.
var animate = func() tea.Cmd {
	return tea.Tick(animInterval, func(time.Time) tea.Msg { return animTickMsg{} })
}

// startAnim starts the frame loop unless it is already running. The loop
// stops itself on the first tick after compiling (and the R overlay's
// waiting) ends.
func (m *Model) startAnim() tea.Cmd {
	if m.animating {
		return nil
	}
	m.animating = true
	return animate()
}

func (m *Model) handleAnimTick() tea.Cmd {
	if !m.compiling && !m.changed.loading && !m.impact.loading && !m.cost.loading && !m.prod.saving && !m.branches.waiting() {
		m.animating = false
		return nil
	}
	m.animFrame++
	return animate()
}

var (
	spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	waveBlocks    = []rune("▁▂▃▄▅▆▇█")
)

// rampColor picks a colour from the looping gradient; i may be negative.
func (s styles) rampColor(i int) lipgloss.Style {
	n := len(s.ramp)
	return lipgloss.NewStyle().Foreground(s.ramp[((i%n)+n)%n])
}

func (m Model) spinner() string {
	f := m.animFrame
	return m.styles.rampColor(f * 2).Bold(true).Render(spinnerFrames[f%len(spinnerFrames)])
}

// flowing renders text with the gradient drifting left to right through it.
func (m Model) flowing(text string) string {
	var b strings.Builder
	for i, r := range []rune(text) {
		b.WriteString(m.styles.rampColor((i - m.animFrame) * 2).Bold(true).Render(string(r)))
	}
	return b.String()
}

// wave is a row of block glyphs rising and falling in the gradient's colours.
func (m Model) wave(width int) string {
	var b strings.Builder
	f := float64(m.animFrame)
	for i := range width {
		h := (math.Sin(float64(i)*0.45-f*0.5) + math.Sin(float64(i)*0.17-f*0.23)) / 2 // -1..1
		lvl := int(math.Round((h + 1) / 2 * float64(len(waveBlocks)-1)))
		b.WriteString(m.styles.rampColor((i - m.animFrame) * 2).Render(string(waveBlocks[lvl])))
	}
	return b.String()
}

// compilingBody fills the action card while the first compile runs.
func (m Model) compilingBody(width int) string {
	elapsed := m.deps.Now().Sub(m.compileStarted).Seconds()
	head := m.spinner() + " " + m.flowing("compiling project") + m.styles.dim.Render(fmt.Sprintf("  %.1fs", elapsed))
	return head + "\n" + m.wave(min(width, 40))
}

// waitingLine fills the Lineage and SQL panes while the first compile runs.
func (m Model) waitingLine() string {
	return m.spinner() + " " + m.styles.dim.Render("waiting for compile…")
}

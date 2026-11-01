package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// colorOptions has the TUI draw in true colour inside tmux when the terminal
// tmux runs in has it. tmux's own TERM (screen-256color, tmux-256color)
// says 256 colours, so the colours would be cut down to the palette, whose
// shades the terminal's theme may redefine; tmux passes true colour through
// to a terminal with the RGB feature, which it finds for itself.
func colorOptions() []tea.ProgramOption {
	if os.Getenv("TMUX") == "" || os.Getenv("NO_COLOR") != "" {
		return nil
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{client_termfeatures}").Output()
	if err != nil || !hasRGB(string(out)) {
		return nil
	}
	return []tea.ProgramOption{tea.WithColorProfile(colorprofile.TrueColor)}
}

// hasRGB reports whether tmux's client_termfeatures, a comma-separated
// list, has RGB.
func hasRGB(features string) bool {
	return slices.Contains(strings.Split(strings.TrimSpace(features), ","), "RGB")
}

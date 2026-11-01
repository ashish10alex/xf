//go:build !darwin && !linux

package web

import (
	"errors"
	"os"
	"os/exec"
)

// termSupported says the terminal tab works here: xf has a pseudo-terminal
// for darwin and linux alone.
const termSupported = false

var errNoTerminal = errors.New("the terminal tab needs macOS or Linux")

func startShell(string, []string, uint16, uint16) (*os.File, *exec.Cmd, error) {
	return nil, nil, errNoTerminal
}

func setPTYSize(*os.File, uint16, uint16) error { return errNoTerminal }

func stopShell(*exec.Cmd) {}

func foreground(*os.File) (int, bool) { return 0, false }

func procName(int) string { return "" }

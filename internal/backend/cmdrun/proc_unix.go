//go:build !windows

package cmdrun

import (
	"os"
	"os/exec"
	"syscall"
)

// isolate puts the run in its own process group: it can be signalled as a
// group, and a terminal ctrl+c aimed at the TUI never reaches it directly.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processTree is a run's process group.
type processTree struct{ pgid int }

func attach(p *os.Process) (*processTree, error) {
	return &processTree{pgid: p.Pid}, nil
}

// interrupt sends the group SIGINT.
func (t *processTree) interrupt() { _ = syscall.Kill(-t.pgid, syscall.SIGINT) }

// kill sends the group SIGKILL.
func (t *processTree) kill() { _ = syscall.Kill(-t.pgid, syscall.SIGKILL) }

// release leaves what is left of the group running: a process group holds
// nothing to let go of.
func (t *processTree) release() {}

// exitCanceled is never a Unix exit status: a process that SIGINT ended
// exits 130 (or reports the signal) on its own.
const exitCanceled = -2

// sendCtrlC is the Windows Ctrl+C helper, not used on Unix.
func sendCtrlC(int) int { return 2 }

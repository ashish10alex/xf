//go:build darwin || linux

package web

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// termSupported says the terminal tab works here: xf has a pseudo-terminal
// for darwin and linux alone.
const termSupported = true

// ptyControl runs f on m's descriptor without taking m out of the
// runtime's poller, so closing m still ends a read waiting on it.
func ptyControl(m *os.File, f func(fd int)) error {
	rc, err := m.SyscallConn()
	if err != nil {
		return err
	}
	return rc.Control(func(fd uintptr) { f(int(fd)) })
}

// startShell starts the user's shell in dir on a new pseudo-terminal of
// rows by cols, with env added to xf's own, and answers its master.
func startShell(dir string, env []string, rows, cols uint16) (*os.File, *exec.Cmd, error) {
	m, name, err := openPTY()
	if err != nil {
		return nil, nil, err
	}
	tty, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	defer func() { _ = tty.Close() }() // the shell holds its own
	_ = setPTYSize(m, rows, cols)
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	// A login shell, as a terminal app starts one, so the profile's PATH
	// (gcloud, bq) is there.
	cmd := exec.Command(shell, "-l")
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor"), env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		_ = m.Close()
		return nil, nil, err
	}
	return m, cmd, nil
}

// setPTYSize tells the terminal its size; the shell's foreground program
// gets SIGWINCH.
func setPTYSize(m *os.File, rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return nil
	}
	var ierr error
	err := ptyControl(m, func(fd int) {
		ierr = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	})
	if err != nil {
		return err
	}
	return ierr
}

// stopShell hangs up on the shell's session, as closing a terminal
// window does, and kills what is left of it after a second.
func stopShell(cmd *exec.Cmd) {
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	time.AfterFunc(time.Second, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
}

// foreground is the process group in front on m's terminal: the shell's
// own while it waits at its prompt, a command's while one runs.
func foreground(m *os.File) (int, bool) {
	var pgrp int
	var ierr error
	if err := ptyControl(m, func(fd int) { pgrp, ierr = unix.IoctlGetInt(fd, unix.TIOCGPGRP) }); err != nil || ierr != nil {
		return 0, false
	}
	return pgrp, true
}

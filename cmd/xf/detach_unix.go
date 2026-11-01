//go:build !windows

package main

import (
	"log"
	"os/exec"
	"syscall"
)

// startDetached starts cmd in a session of its own, so it outlives this
// process and its terminal, and returns it.
func startDetached(cmd *exec.Cmd) (*exec.Cmd, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, cmd.Start()
}

// prepareServe readies a starting xf serve's process: nothing on Unix.
func prepareServe() {}

// serveFromCopy runs xf serve from a copy of xf on Windows; elsewhere an
// upgrade can replace a running executable, so it reports false.
func serveFromCopy([]string, *log.Logger) bool { return false }

//go:build windows

package main

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startDetached starts cmd with no console and outside this process's Job
// Object, so it outlives this process: an SSH session or a terminal kills
// its job's processes when it closes. Where the job forbids breaking away,
// it is started detached only, as a copy of cmd (a Cmd starts only once),
// which it returns: wait on the Cmd returned.
func startDetached(cmd *exec.Cmd) (*exec.Cmd, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_BREAKAWAY_FROM_JOB}
	err := cmd.Start()
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return cmd, err
	}
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.Args[0] = cmd.Args[0]
	retry.Dir, retry.Env = cmd.Dir, cmd.Env
	retry.Stdin, retry.Stdout, retry.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	retry.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	return retry, retry.Start()
}

var setConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")

// prepareServe clears the "ignore Ctrl+C" flag a starting xf serve may have
// inherited (SSH sessions and Node's detached spawns pass it on). The runs
// it starts inherit it in turn, and a run that ignores Ctrl+C can't be
// asked to stop.
func prepareServe() {
	_, _, _ = setConsoleCtrlHandler.Call(0, 0)
}

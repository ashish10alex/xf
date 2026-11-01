//go:build !windows

package cmdrun

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeGit puts a git on PATH that starts a background process, as git
// starts its file-system monitor daemon, and writes its pid to the file it
// returns.
func fakeGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "daemon.pid")
	script := "#!/bin/sh\nsleep 30 >/dev/null 2>&1 &\necho $! > " + pidFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return pidFile
}

func daemonPid(t *testing.T, pidFile string) int {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// alive reports whether pid is still running a while after the call: a
// killed process can take a moment to go.
func alive(pid int) bool {
	for range 20 {
		var ws syscall.WaitStatus
		if syscall.Kill(pid, 0) != nil {
			return false
		}
		_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		time.Sleep(10 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) == nil
}

// What git leaves running when it exits by itself goes on, so its
// file-system monitor daemon is started once, not by every call.
func TestGitLeavesItsDaemon(t *testing.T) {
	pidFile := fakeGit(t)
	if _, err := Git(context.Background(), t.TempDir(), "show", ":./x").Output(); err != nil {
		t.Fatal(err)
	}
	if pid := daemonPid(t, pidFile); !alive(pid) {
		t.Error("the daemon git started was killed with it")
	}
}

// Other commands still stop their whole tree as they exit.
func TestCommandStopsItsTree(t *testing.T) {
	pidFile := fakeGit(t)
	if err := Command(context.Background(), "git").Run(); err != nil {
		t.Fatal(err)
	}
	if pid := daemonPid(t, pidFile); alive(pid) {
		t.Error("a command's background process outlived it")
	}
}

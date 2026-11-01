package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
)

// ShortTempDir is a temp directory for tests that put a Unix socket in it:
// on macOS the default temp directory is too long for a socket path, so
// there it's under /tmp. It's removed when the test ends.
func ShortTempDir(t testing.TB) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "xf-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Symlink makes newname a symlink to oldname, skipping the test where the
// OS doesn't let this user create symlinks (Windows without Developer Mode
// or admin rights).
func Symlink(t testing.TB, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("can't create symlinks here: %v", err)
		}
		t.Fatal(err)
	}
}

// SocketPath is somewhere a test can listen for xf serve's clients: a Unix
// socket in a ShortTempDir, or on Windows a named pipe no other test uses.
func SocketPath(t testing.TB) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return `\\.\pipe\xf-test-` + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(pipeSeq.Add(1), 10)
	}
	return filepath.Join(ShortTempDir(t), "s.sock")
}

var pipeSeq atomic.Int64

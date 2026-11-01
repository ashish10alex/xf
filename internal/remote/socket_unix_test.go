//go:build !windows

package remote

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func checkSocketShape(t *testing.T, path string) {
	t.Helper()
	if len(path) > maxSocketPath || !strings.HasPrefix(filepath.Base(path), "xf-") || !strings.HasSuffix(path, fmt.Sprintf("-v%d.sock", ProtocolVersion)) {
		t.Errorf("unexpected path %s", path)
	}
}

// A temp dir too long for a socket path falls back to /tmp.
func TestSocketPathFallsBackToTmp(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", "/"+strings.Repeat("x", 120))
	if got := SocketPath(root, "dataform"); !strings.HasPrefix(got, "/tmp/xf-") {
		t.Errorf("got %s, want a /tmp fallback", got)
	}
}

// A socket nobody listens on, left by a crash, is replaced.
func TestListenReplacesStaleSocket(t *testing.T) {
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	ln, err = Listen(path)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	_ = ln.Close()
}

//go:build !windows

package remote

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ashish10alex/xf/internal/project"
)

// maxSocketPath keeps socket paths under the sun_path limit (104 bytes on
// macOS, 108 on Linux).
const maxSocketPath = 103

// SocketPath is where xf serve listens for the project at root, compiled
// with backend (backend.EngineName): a Unix socket named by socketName
// from realpath(root), in the temp dir, or /tmp if that is too long. A
// directory that is both a Dataform and a dbt project has a server for
// each, and so does each variant of a backend (its binary, dbt's target,
// ...).
func SocketPath(root, backend string) string {
	name := socketName(project.RealPath(root), backend) + ".sock"
	if p := filepath.Join(os.TempDir(), name); len(p) <= maxSocketPath {
		return p
	}
	return filepath.Join("/tmp", name)
}

// LogPath is where a xf serve started by a client writes its log: beside
// its socket.
func LogPath(root, backend string) string {
	return strings.TrimSuffix(SocketPath(root, backend), ".sock") + ".log"
}

// Listen listens on the socket at path, replacing a socket left over from a
// crash. It fails with ErrRunning when a live server has it. A lock file
// beside the socket keeps two servers starting at once from both listening.
func Listen(path string) (net.Listener, error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	defer func() { _ = lock.Close() }() // also releases the lock
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if c, err := net.Dial("unix", path); err == nil {
		_ = c.Close()
		return nil, ErrRunning
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	return ln, nil
}

// dialOnce makes one attempt to connect to the socket at path.
func dialOnce(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// serving reports whether a failed dial means a server is there but not
// accepting yet, so Dialer should wait rather than start another. A Unix
// socket either accepts or isn't there.
func serving(error) bool { return false }

// refused reports whether a failed dial means Dialer must give up: never
// for a Unix socket in the user's own temp dir.
func refused(error) bool { return false }

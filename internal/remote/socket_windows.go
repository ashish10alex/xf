//go:build windows

package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/ashish10alex/xf/internal/project"
)

// SocketPath is the named pipe xf serve listens on for the project at root,
// compiled with backend (backend.EngineName). Pipe names are shared by
// every user on the machine, so the name's hash covers the user's SID as
// well as realpath(root): two users opening one checkout get a server
// each.
func SocketPath(root, backend string) string {
	sid, err := currentUserSID()
	user := ""
	if err == nil {
		user = sid.String()
	}
	return `\\.\pipe\` + socketName(user+"\n"+project.RealPath(root), backend)
}

// LogPath is where a xf serve started by a client writes its log: in the
// user's temp dir, named after its pipe.
func LogPath(root, backend string) string {
	return filepath.Join(os.TempDir(), filepath.Base(SocketPath(root, backend))+".log")
}

// listenRetry bounds how long Listen retries a pipe name that exists but
// has no server: a server that died leaves its name until every client has
// closed its end.
const listenRetry = 2 * time.Second

// pipeBuffer is the size of each direction's buffer in a pipe.
const pipeBuffer = 64 << 10

// dialTimeout bounds one dial: dialing a pipe that exists but isn't
// accepting waits until the context ends.
const dialTimeout = 2 * time.Second

// Listen listens on the named pipe at path, which only the current user can
// open. It fails with ErrRunning when a server of this user has it: one
// that answers, or one whose pipe still exists after listenRetry (a pipe
// only takes connections while its server is in Accept).
// Creating the pipe's first instance fails while the name exists, so two
// servers starting at once can't both listen, and no lock file is needed.
func Listen(path string) (net.Listener, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	cfg := &winio.PipeConfig{
		SecurityDescriptor: fmt.Sprintf("O:%sD:P(A;;GA;;;%s)", sid, sid),
		// Without buffers a write waits until the other end reads, as
		// a Unix socket's doesn't.
		InputBufferSize:  pipeBuffer,
		OutputBufferSize: pipeBuffer,
	}
	deadline := time.Now().Add(listenRetry)
	for {
		ln, err := winio.ListenPipe(path, cfg)
		if err == nil {
			return ln, nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, fmt.Errorf("listen on %s: %w", path, err)
		}
		// The name exists: a live server, another user's pipe, or a dead
		// server's pipe that its clients haven't let go of yet.
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		c, derr := dialOnce(ctx, path)
		cancel()
		switch {
		case derr == nil:
			_ = c.Close()
			return nil, ErrRunning
		case refused(derr):
			return nil, derr
		case time.Now().After(deadline):
			// Still there and still not taking connections: a server of
			// ours that hasn't called Accept yet, or is stuck. Either way
			// it isn't ours to replace.
			return nil, ErrRunning
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// errNotOurs means the pipe at a server's name doesn't belong to the
// current user alone: another user created it first.
var errNotOurs = errors.New("the xf pipe for this project belongs to another user")

// dialOnce makes one attempt, of at most dialTimeout, to connect to the
// pipe at path, and only accepts a pipe that the current user owns and
// nobody else can open.
func dialOnce(ctx context.Context, path string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	c, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := checkOwner(c); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// serving reports whether a failed dial means the pipe exists but isn't
// accepting (a server starting up, or busy), so Dialer waits for it rather
// than starting another.
func serving(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, windows.ERROR_PIPE_BUSY)
}

// refused reports whether a failed dial means Dialer must give up: the
// pipe is another user's.
func refused(err error) bool {
	return errors.Is(err, errNotOurs) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// checkOwner fails unless the pipe c is connected to is owned by the
// current user and lets nobody else in.
func checkOwner(c net.Conn) error {
	fc, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("can't check the owner of a %T", c)
	}
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(fc.Fd()), windows.SE_KERNEL_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !owner.Equals(me) {
		return errNotOurs
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errNotOurs
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		if !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(me) {
			return errNotOurs
		}
	}
	return nil
}

var currentUserSID = sync.OnceValues(func() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid.Copy()
})

//go:build windows

package remote

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func checkSocketShape(t *testing.T, path string) {
	t.Helper()
	if !strings.HasPrefix(path, `\\.\pipe\xf-`) || !strings.HasSuffix(path, fmt.Sprintf("-v%d", ProtocolVersion)) {
		t.Errorf("unexpected pipe %s", path)
	}
}

// A pipe that lets other users in isn't trusted: another user may have
// created it first to pose as the server.
func TestDialRefusesAPipeOthersCanOpen(t *testing.T) {
	path := socketPath(t)
	ln, err := winio.ListenPipe(path, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;WD)"}) // Everyone
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if _, err := Dialer(path, func() error { return errors.New("started") })(t.Context()); !errors.Is(err, errNotOurs) {
		t.Errorf("dial = %v, want errNotOurs", err)
	}
	if _, err := Listen(path); !errors.Is(err, errNotOurs) {
		t.Errorf("listen = %v, want errNotOurs", err)
	}
}

// A server's pipe name outlives it while a client still holds the pipe
// open: a new server must still get to listen.
func TestListenAfterAServerDiesWithClientsConnected(t *testing.T) {
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	c, err := dialOnce(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	// The server dies, closing its end; the client hasn't noticed yet.
	_ = (<-accepted).Close()
	_ = ln.Close()
	go func() { time.Sleep(300 * time.Millisecond); _ = c.Close() }()
	ln2, err := Listen(path)
	if err != nil {
		t.Fatalf("a new server couldn't listen: %v", err)
	}
	_ = ln2.Close()
}

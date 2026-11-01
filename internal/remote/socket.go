package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// ProtocolVersion is part of the socket's name, so a client only ever meets
// a server that speaks its protocol. Bump it whenever a message changes.
const ProtocolVersion = 21

// socketName is the name of the socket (Unix) or pipe (Windows) xf serve
// listens on for a project: "xf-", the first 16 hex characters of the
// sha256 of key (the project's real path, and on Windows the user too),
// the backend (backend.EngineName, so each variant of a backend has its own
// server) and the protocol version.
func socketName(key, backend string) string {
	sum := sha256.Sum256([]byte(key))
	return "xf-" + hex.EncodeToString(sum[:])[:16] + "-" + backend + "-v" + strconv.Itoa(ProtocolVersion)
}

// ErrRunning means a server is already listening on the socket.
var ErrRunning = errors.New("a xf server is already running for this project")

// startWait bounds how long Dialer waits for a server it started.
const startWait = 10 * time.Second

// Dialer connects to the server on the socket at path, calling start to
// run one first when nobody is listening.
func Dialer(path string, start func() error) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		c, err := dialOnce(ctx, path)
		if err == nil {
			return c, nil
		}
		if refused(err) {
			return nil, err
		}
		if !serving(err) {
			if err := start(); err != nil {
				return nil, fmt.Errorf("start xf serve: %w", err)
			}
		}
		deadline := time.Now().Add(startWait)
		for {
			c, err := dialOnce(ctx, path)
			if err == nil {
				return c, nil
			}
			if refused(err) {
				return nil, err
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("xf serve did not start listening on %s: %w", path, err)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
}

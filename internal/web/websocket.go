package web

// The event stream is a WebSocket (RFC 6455), not server-sent events: a
// browser lets a page have six HTTP/1.1 connections to one host and port,
// shared by all its tabs, and each tab's event stream would hold one for
// good, so a few tabs left the rest of every tab's requests waiting.
// WebSockets are counted apart from them. Only what the stream needs is
// here: the server sends text frames, answers pings and closes, and pings
// the page itself; whatever the page sends is read and dropped.

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// The frame opcodes.
const (
	wsText   = 0x1
	wsBinary = 0x2
	wsClose  = 0x8
	wsPing   = 0x9
	wsPong   = 0xA
)

const (
	// wsWriteTimeout bounds a frame's write to a page that stopped reading.
	wsWriteTimeout = 10 * time.Second
	// wsReadTimeout is how long a page may say nothing, not even a pong to
	// the server's pings, before it counts as gone.
	wsReadTimeout = 3 * keepAlive
	// wsMaxFrame is the most a page's frame may carry: it has nothing to
	// say but a close or a ping.
	wsMaxFrame = 4096
)

// wsConn is a page's WebSocket. Its frames are sent one at a time; done
// is closed once the page has closed it or gone.
type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex // held while a frame is written
	done chan struct{}
}

// headerHas reports whether one of the comma-separated tokens of r's
// header name is token, in any case.
func headerHas(r *http.Request, name, token string) bool {
	for _, v := range r.Header.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// wsAccept is the Sec-WebSocket-Accept answering key.
func wsAccept(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// upgradeWS answers r's WebSocket handshake and takes its connection over,
// or answers with an error and returns nil.
func upgradeWS(w http.ResponseWriter, r *http.Request) *wsConn {
	key := r.Header.Get("Sec-WebSocket-Key")
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 ||
		!headerHas(r, "Connection", "upgrade") || !headerHas(r, "Upgrade", "websocket") {
		http.Error(w, "this is a WebSocket", http.StatusBadRequest)
		return nil
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "WebSocket version 13 only", http.StatusUpgradeRequired)
		return nil
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil
	}
	_ = conn.SetDeadline(time.Time{}) // the server's own, for the request
	c := &wsConn{conn: conn, r: rw.Reader, done: make(chan struct{})}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n"
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if _, err := io.WriteString(conn, resp); err != nil {
		_ = conn.Close()
		return nil
	}
	go c.read()
	return c
}

// write sends one unfragmented frame.
func (c *wsConn) write(op byte, payload []byte) error {
	head := make([]byte, 2, 10)
	head[0] = 0x80 | op
	switch n := len(payload); {
	case n < 126:
		head[1] = byte(n)
	case n <= 0xFFFF:
		head[1] = 126
		head = binary.BigEndian.AppendUint16(head, uint16(n))
	default:
		head[1] = 127
		head = binary.BigEndian.AppendUint64(head, uint64(n))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if _, err := c.conn.Write(append(head, payload...)); err != nil {
		return err
	}
	return nil
}

// close sends a close frame with code and ends the connection.
func (c *wsConn) close(code uint16) {
	_ = c.write(wsClose, binary.BigEndian.AppendUint16(nil, code))
	_ = c.conn.Close()
}

// read reads the page's frames until it closes the socket or goes:
// a ping is answered, a close is answered and ends it.
func (c *wsConn) read() {
	defer close(c.done)
	defer func() { _ = c.conn.Close() }()
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		op, payload, err := c.readFrame()
		if err != nil {
			if errors.Is(err, errWSFrame) {
				_ = c.write(wsClose, binary.BigEndian.AppendUint16(nil, 1002))
			}
			return
		}
		switch op {
		case wsPing:
			if c.write(wsPong, payload) != nil {
				return
			}
		case wsClose:
			code := payload
			if len(code) > 2 {
				code = code[:2]
			}
			_ = c.write(wsClose, code)
			return
		}
	}
}

var errWSFrame = errors.New("bad WebSocket frame")

// readFrame reads one of the page's frames, its payload unmasked.
func (c *wsConn) readFrame() (op byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(c.r, head[:]); err != nil {
		return 0, nil, err
	}
	op = head[0] & 0x0F
	if head[1]&0x80 == 0 { // a page's frames are masked
		return 0, nil, errWSFrame
	}
	n := uint64(head[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(c.r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(c.r, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > wsMaxFrame || op >= wsClose && n > 125 {
		return 0, nil, errWSFrame
	}
	var mask [4]byte
	if _, err = io.ReadFull(c.r, mask[:]); err != nil {
		return 0, nil, err
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return op, payload, nil
}

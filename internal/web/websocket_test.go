package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/frontend"
)

// testWS is a test's WebSocket client: just enough to read the event
// stream and poke at the server's frames.
type testWS struct {
	conn net.Conn
	r    *bufio.Reader
	res  *http.Response
}

// dialWS opens a WebSocket to path on base (an httptest server's URL),
// with c's cookies and the extra headers; on a refused handshake it
// returns the response with a nil conn.
func dialWS(t *testing.T, c *http.Client, base, path string, extra http.Header) *testWS {
	t.Helper()
	u, err := url.Parse(base + path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	key := base64.StdEncoding.EncodeToString(raw)
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	for k, vs := range extra {
		req.Header[k] = vs
	}
	if c != nil && c.Jar != nil {
		for _, ck := range c.Jar.Cookies(u) {
			req.AddCookie(ck)
		}
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	res, err := http.ReadResponse(r, req)
	if err != nil {
		t.Fatal(err)
	}
	ws := &testWS{r: r, res: res}
	if res.StatusCode != http.StatusSwitchingProtocols {
		return ws
	}
	if got := res.Header.Get("Sec-WebSocket-Accept"); got != wsAccept(key) {
		t.Fatalf("Sec-WebSocket-Accept %q, want %q", got, wsAccept(key))
	}
	ws.conn = conn
	return ws
}

// next reads the server's next frame.
func (w *testWS) next(t *testing.T) (byte, []byte) {
	t.Helper()
	_ = w.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var head [2]byte
	if _, err := io.ReadFull(w.r, head[:]); err != nil {
		t.Fatal(err)
	}
	if head[1]&0x80 != 0 {
		t.Fatal("the server masked a frame")
	}
	n := uint64(head[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		_, _ = io.ReadFull(w.r, b[:])
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		_, _ = io.ReadFull(w.r, b[:])
		n = binary.BigEndian.Uint64(b[:])
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(w.r, p); err != nil {
		t.Fatal(err)
	}
	return head[0] & 0x0F, p
}

// send writes a frame, masked as a browser's are unless unmasked.
func (w *testWS) send(t *testing.T, op byte, payload []byte, unmasked bool) {
	t.Helper()
	b := []byte{0x80 | op, byte(len(payload))}
	if unmasked {
		b = append(b, payload...)
	} else {
		mask := []byte{1, 2, 3, 4}
		b[1] |= 0x80
		b = append(b, mask...)
		for i, c := range payload {
			b = append(b, c^mask[i%4])
		}
	}
	if _, err := w.conn.Write(b); err != nil {
		t.Fatal(err)
	}
}

// eventStream reads a page's events.
type eventStream struct {
	ws *testWS
	ch chan [2]string // name, data
}

// openEvents opens the event stream of the project with key, and waits
// for its hello.
func openEvents(t *testing.T, c *http.Client, base, key string) *eventStream {
	t.Helper()
	return streamEvents(t, c, base, "/api/events?project="+url.QueryEscape(key))
}

func streamEvents(t *testing.T, c *http.Client, base, path string) *eventStream {
	t.Helper()
	ws := dialWS(t, c, base, path, nil)
	if ws.conn == nil {
		t.Fatalf("the event stream answered %s", ws.res.Status)
	}
	s := &eventStream{ws: ws, ch: make(chan [2]string, 64)}
	go func() {
		for {
			var head [2]byte
			if _, err := io.ReadFull(ws.r, head[:]); err != nil {
				return
			}
			n := uint64(head[1] & 0x7F)
			switch n {
			case 126:
				var b [2]byte
				_, _ = io.ReadFull(ws.r, b[:])
				n = uint64(binary.BigEndian.Uint16(b[:]))
			case 127:
				var b [8]byte
				_, _ = io.ReadFull(ws.r, b[:])
				n = binary.BigEndian.Uint64(b[:])
			}
			p := make([]byte, n)
			if _, err := io.ReadFull(ws.r, p); err != nil {
				return
			}
			if head[0]&0x0F != wsText {
				continue
			}
			var m struct {
				Event string          `json:"event"`
				Data  json.RawMessage `json:"data"`
			}
			if json.Unmarshal(p, &m) == nil {
				s.ch <- [2]string{m.Event, string(m.Data)}
			}
		}
	}()
	s.until(t, "hello", "")
	return s
}

// close ends the stream, as a page going does.
func (s *eventStream) close() { _ = s.ws.conn.Close() }

// until waits for an event named name whose data holds text, and returns
// its data.
func (s *eventStream) until(t *testing.T, name, text string) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.ch:
			if ev[0] == name && strings.Contains(ev[1], text) {
				return ev[1]
			}
		case <-timeout:
			t.Fatalf("no %s event with %q", name, text)
		}
	}
}

// The handshake's accept value is RFC 6455's own example.
func TestWSAccept(t *testing.T) {
	if got := wsAccept("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Errorf("wsAccept = %q", got)
	}
}

// The stream answers a ping with a pong and a close with a close, and
// closes on a frame a browser wouldn't send (unmasked).
func TestWSControlFrames(t *testing.T) {
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	ws := dialWS(t, c, ts.URL, "/api/events", nil)
	if ws.conn == nil {
		t.Fatalf("handshake answered %s", ws.res.Status)
	}
	if op, p := ws.next(t); op != wsText || !strings.Contains(string(p), `"event":"hello"`) {
		t.Fatalf("first frame %d %q, want the hello", op, p)
	}
	ws.send(t, wsPing, []byte("hi"), false)
	if op, p := ws.next(t); op != wsPong || string(p) != "hi" {
		t.Errorf("ping answered %d %q, want pong \"hi\"", op, p)
	}
	ws.send(t, wsClose, binary.BigEndian.AppendUint16(nil, 1000), false)
	if op, p := ws.next(t); op != wsClose || len(p) < 2 || binary.BigEndian.Uint16(p) != 1000 {
		t.Errorf("close answered %d %v, want close 1000", op, p)
	}

	ws = dialWS(t, c, ts.URL, "/api/events", nil)
	ws.next(t) // hello
	ws.send(t, wsPing, nil, true)
	if op, p := ws.next(t); op != wsClose || len(p) < 2 || binary.BigEndian.Uint16(p) != 1002 {
		t.Errorf("an unmasked frame got %d %v, want close 1002", op, p)
	}
}

// The handshake goes through the guard: another site's page is refused,
// and a request that isn't a WebSocket is told so.
func TestWSRefused(t *testing.T) {
	_, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph())})
	ws := dialWS(t, c, ts.URL, "/api/events", http.Header{"Origin": {"https://evil.example"}})
	if ws.res.StatusCode != http.StatusForbidden {
		t.Errorf("another site's origin: %s, want 403", ws.res.Status)
	}
	ws = dialWS(t, c, ts.URL, "/api/events", http.Header{"Sec-Fetch-Site": {"cross-site"}})
	if ws.res.StatusCode != http.StatusForbidden {
		t.Errorf("a cross-site request: %s, want 403", ws.res.Status)
	}
	res, err := c.Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("a plain GET: %s, want 400", res.Status)
	}
}

// A page hears that xf web is stopping before its stream ends, as when
// ctrl+c stops it in its terminal, so it can say so.
func TestStreamSaysStopping(t *testing.T) {
	s, err := New(Config{Project: &Project{Deps: frontend.Deps{Root: "/work/proj", Engine: newFakeEngine(testGraph())}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	l, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, l) }()
	ev := streamEvents(t, nil, "http://"+l.Addr().String(), "/api/events")
	stop()
	ev.until(t, "stopping", "")
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Serve did not return")
	}
}

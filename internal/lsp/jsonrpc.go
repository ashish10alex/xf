package lsp

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/ashish10alex/xf/internal/jsonrpc"
)

// The server's names for package jsonrpc's framing.
type (
	message  = jsonrpc.Message
	rpcError = jsonrpc.Error
)

const (
	codeInvalidParams  = jsonrpc.CodeInvalidParams
	codeMethodNotFound = jsonrpc.CodeMethodNotFound
	codeInvalidRequest = jsonrpc.CodeInvalidRequest
	// codeRequestFailed is LSP's code for a request that was valid and
	// failed; the editor shows its message.
	codeRequestFailed = -32803
)

// conn reads and writes LSP base-protocol messages. Writes are safe from
// several goroutines.
type conn struct {
	c *jsonrpc.Conn

	// The server's own requests, waiting for the client's answers.
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *message
}

func newConn(r io.Reader, w io.Writer) *conn {
	return &conn{c: jsonrpc.NewConn(r, w), pending: map[int64]chan *message{}}
}

// call sends the client a request and waits for its answer, which the
// message loop hands over with deliver.
func (c *conn) call(ctx context.Context, method string, params any) (*message, error) {
	reply := make(chan *message, 1)
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.pending[id] = reply
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.c.Call(id, method, params); err != nil {
		return nil, err
	}
	select {
	case m := <-reply:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// deliver hands the client's answer m to the call waiting for it.
func (c *conn) deliver(m *message) {
	var id int64
	if json.Unmarshal(m.ID, &id) != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

// read returns the next message, or io.EOF when the input ends cleanly.
func (c *conn) read() (*message, error) { return c.c.Read() }

// reply answers a request with a result (nil encodes as null) or an error.
func (c *conn) reply(id json.RawMessage, result any, rerr *rpcError) error {
	return c.c.Reply(id, result, rerr)
}

func (c *conn) notify(method string, params any) error { return c.c.Notify(method, params) }

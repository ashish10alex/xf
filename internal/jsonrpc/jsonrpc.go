// Package jsonrpc reads and writes JSON-RPC 2.0 messages framed as in the
// Language Server Protocol: a Content-Length header, a blank line, then a
// JSON body. xf lsp speaks it to editors, and xf serve to its clients.
package jsonrpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"sync"
)

// Error codes.
const (
	CodeInvalidParams  = -32602
	CodeMethodNotFound = -32601
	CodeInvalidRequest = -32600
	CodeInternal       = -32603
)

// Message is any incoming message: a request (ID and Method), a
// notification (Method only) or a response (ID with Result or Error).
type Message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// IsRequest reports whether m expects a response.
func (m *Message) IsRequest() bool { return len(m.ID) > 0 && m.Method != "" }

// IsResponse reports whether m answers a request.
func (m *Message) IsResponse() bool { return len(m.ID) > 0 && m.Method == "" }

// Error is a JSON-RPC error. Data carries whatever the method defines.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  any             `json:"params"`
}

// Conn reads and writes messages. Reads are for one goroutine; writes are
// safe from several.
type Conn struct {
	r  *bufio.Reader
	mu sync.Mutex
	w  io.Writer
}

// NewConn reads messages from r and writes them to w.
func NewConn(r io.Reader, w io.Writer) *Conn {
	return &Conn{r: bufio.NewReader(r), w: w}
}

// Read returns the next message. It returns io.EOF when the input ends
// cleanly between messages.
func (c *Conn) Read() (*Message, error) {
	body, err := c.readBody()
	if err != nil {
		return nil, err
	}
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}
	return &m, nil
}

// readBody returns the JSON body of the next message.
func (c *Conn) readBody() ([]byte, error) {
	hdr, err := textproto.NewReader(c.r).ReadMIMEHeader()
	if err != nil {
		if errors.Is(err, io.EOF) && len(hdr) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("read header: %w", err)
	}
	n, err := strconv.Atoi(hdr.Get("Content-Length"))
	if err != nil || n < 0 {
		return nil, fmt.Errorf("bad Content-Length %q", hdr.Get("Content-Length"))
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// Write sends v, which must marshal to a JSON-RPC message.
func (c *Conn) Write(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.w.Write(body)
	return err
}

// Reply answers a request with a result (nil encodes as null) or an error.
func (c *Conn) Reply(id json.RawMessage, result any, rerr *Error) error {
	r := response{JSONRPC: "2.0", ID: id, Error: rerr}
	if rerr == nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		msg := json.RawMessage(raw)
		r.Result = &msg
	}
	return c.Write(r)
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	return c.Write(request{JSONRPC: "2.0", Method: method, Params: params})
}

// Call sends a request with the given id; its response arrives through Read.
func (c *Conn) Call(id int64, method string, params any) error {
	return c.Write(request{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: params})
}

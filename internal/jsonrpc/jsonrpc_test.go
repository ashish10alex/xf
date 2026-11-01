package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestConnRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewConn(nil, &buf)
	if err := w.Notify("x/y", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Reply(json.RawMessage("7"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Call(8, "a/b", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Reply(json.RawMessage("9"), nil, &Error{Code: CodeInternal, Message: "boom"}); err != nil {
		t.Fatal(err)
	}
	r := NewConn(&buf, nil)
	m, err := r.Read()
	if err != nil || m.Method != "x/y" || string(m.Params) != `{"a":1}` || m.IsRequest() || m.IsResponse() {
		t.Fatalf("got %+v, %v", m, err)
	}
	body, err := r.readBody()
	if err != nil || string(body) != `{"jsonrpc":"2.0","id":7,"result":null}` {
		t.Fatalf("got %s, %v", body, err)
	}
	if m, err := r.Read(); err != nil || !m.IsRequest() || string(m.ID) != "8" || m.Method != "a/b" {
		t.Fatalf("call: got %+v, %v", m, err)
	}
	if m, err := r.Read(); err != nil || !m.IsResponse() || m.Error == nil || m.Error.Message != "boom" {
		t.Fatalf("error reply: got %+v, %v", m, err)
	}
	if _, err := r.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want EOF", err)
	}
	if _, err := NewConn(strings.NewReader("Content-Length: x\r\n\r\n{}"), nil).Read(); err == nil {
		t.Fatal("bad Content-Length accepted")
	}
}

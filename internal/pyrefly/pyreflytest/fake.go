// Package pyreflytest is a stand-in for `pyrefly lsp` in tests: a test
// binary that runs Serve when Env is set acts as pyrefly.
package pyreflytest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/ashish10alex/xf/internal/jsonrpc"
)

// Env, set to "1", makes a test binary that calls Main act as pyrefly.
const Env = "XF_FAKE_PYREFLY"

// LibEnv is a file the fake says "lib" is defined in, at its first line.
const LibEnv = "XF_FAKE_PYREFLY_LIB"

// Main runs Serve on stdin and stdout and exits, when Env is set; a
// TestMain calls it first.
func Main() {
	if os.Getenv(Env) != "1" {
		return
	}
	Serve(os.Stdin, os.Stdout)
	os.Exit(0)
}

// Serve answers as pyrefly would, simply:
//
//   - each line with BAD in it is an error, whose message names the
//     interpreter the client gave ("bad line (python: …)")
//   - hover is the word at the place, in a python code block, and a link
//     to $XF_FAKE_PYREFLY_LIB's line 1, column 5
//   - "lib" is defined in $XF_FAKE_PYREFLY_LIB, anything else at the start
//     of its own file
//   - completion offers alpha and beta; signature help is f(a, b) on b
//
// It asks for the interpreter after initialized and after each
// didChangeConfiguration, and publishes every open file's errors again.
func Serve(r io.Reader, w io.Writer) {
	c := jsonrpc.NewConn(r, w)
	var mu sync.Mutex
	docs := map[string]string{}
	versions := map[string]int{}
	python := ""
	asks := 0
	publish := func(uri string) {
		mu.Lock()
		text, v, py := docs[uri], versions[uri], python
		mu.Unlock()
		ds := []any{}
		for i, line := range strings.Split(text, "\n") {
			if at := strings.Index(line, "BAD"); at >= 0 {
				ds = append(ds, map[string]any{
					"range":    map[string]any{"start": map[string]int{"line": i, "character": at}, "end": map[string]int{"line": i, "character": at + 3}},
					"severity": 1, "code": "bad-line", "message": fmt.Sprintf("bad line (python: %s)", py),
				})
			}
		}
		_ = c.Notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "version": v, "diagnostics": ds})
	}
	ask := func() {
		mu.Lock()
		asks++
		id := asks
		mu.Unlock()
		_ = c.Write(map[string]any{"jsonrpc": "2.0", "id": fmt.Sprintf("cfg-%d", id), "method": "workspace/configuration",
			"params": map[string]any{"items": []any{map[string]any{"section": "python"}}}})
	}
	type posParams struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"position"`
	}
	wordAt := func(p posParams) (string, int, int) {
		mu.Lock()
		text := docs[p.TextDocument.URI]
		mu.Unlock()
		lines := strings.Split(text, "\n")
		if p.Position.Line >= len(lines) {
			return "", 0, 0
		}
		line := lines[p.Position.Line]
		isWord := func(b byte) bool {
			return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
		}
		a, b := min(p.Position.Character, len(line)), min(p.Position.Character, len(line))
		for a > 0 && isWord(line[a-1]) {
			a--
		}
		for b < len(line) && isWord(line[b]) {
			b++
		}
		return line[a:b], a, b
	}
	for {
		m, err := c.Read()
		if err != nil {
			return
		}
		if m.IsResponse() {
			var res []struct {
				PythonPath string `json:"pythonPath"`
			}
			if json.Unmarshal(m.Result, &res) == nil && len(res) == 1 {
				mu.Lock()
				python = res[0].PythonPath
				open := make([]string, 0, len(docs))
				for u := range docs {
					open = append(open, u)
				}
				mu.Unlock()
				for _, u := range open {
					publish(u)
				}
			}
			continue
		}
		switch m.Method {
		case "initialize":
			_ = c.Reply(m.ID, map[string]any{"capabilities": map[string]any{"textDocumentSync": 1}}, nil)
		case "initialized":
			ask()
		case "workspace/didChangeConfiguration":
			ask()
		case "textDocument/didOpen":
			var p struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
					Text    string `json:"text"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(m.Params, &p)
			mu.Lock()
			docs[p.TextDocument.URI], versions[p.TextDocument.URI] = p.TextDocument.Text, p.TextDocument.Version
			mu.Unlock()
			publish(p.TextDocument.URI)
		case "textDocument/didChange":
			var p struct {
				TextDocument struct {
					URI     string `json:"uri"`
					Version int    `json:"version"`
				} `json:"textDocument"`
				ContentChanges []struct {
					Text string `json:"text"`
				} `json:"contentChanges"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if len(p.ContentChanges) == 0 {
				continue
			}
			mu.Lock()
			docs[p.TextDocument.URI], versions[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text, p.TextDocument.Version
			mu.Unlock()
			publish(p.TextDocument.URI)
		case "textDocument/didClose":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(m.Params, &p)
			mu.Lock()
			delete(docs, p.TextDocument.URI)
			mu.Unlock()
		case "textDocument/hover":
			var p posParams
			_ = json.Unmarshal(m.Params, &p)
			word, a, b := wordAt(p)
			if word == "" {
				_ = c.Reply(m.ID, nil, nil)
				continue
			}
			md := "```python\n" + word + "\n```\nA word."
			if lib := os.Getenv(LibEnv); lib != "" {
				md += "\n\nGo to [lib](" + (&url.URL{Scheme: "file", Path: lib}).String() + "#L1,5)"
			}
			_ = c.Reply(m.ID, map[string]any{
				"contents": map[string]any{"kind": "markdown", "value": md},
				"range":    map[string]any{"start": map[string]int{"line": p.Position.Line, "character": a}, "end": map[string]int{"line": p.Position.Line, "character": b}},
			}, nil)
		case "textDocument/definition":
			var p posParams
			_ = json.Unmarshal(m.Params, &p)
			word, _, _ := wordAt(p)
			uri := p.TextDocument.URI
			if lib := os.Getenv(LibEnv); word == "lib" && lib != "" {
				uri = (&url.URL{Scheme: "file", Path: lib}).String()
			}
			_ = c.Reply(m.ID, []any{map[string]any{"uri": uri, "range": map[string]any{"start": map[string]int{"line": 0, "character": 4}, "end": map[string]int{"line": 0, "character": 7}}}}, nil)
		case "textDocument/completion":
			_ = c.Reply(m.ID, map[string]any{"isIncomplete": false, "items": []any{
				map[string]any{"label": "alpha", "kind": 6},
				map[string]any{"label": "beta", "kind": 3, "detail": "() -> int"},
			}}, nil)
		case "textDocument/signatureHelp":
			_ = c.Reply(m.ID, map[string]any{"signatures": []any{map[string]any{
				"label": "f(a, b)", "parameters": []any{map[string]any{"label": "a"}, map[string]any{"label": []int{5, 6}}},
			}}, "activeSignature": 0, "activeParameter": 1}, nil)
		case "shutdown":
			_ = c.Reply(m.ID, nil, nil)
		case "exit":
			return
		default:
			if m.IsRequest() {
				_ = c.Reply(m.ID, nil, nil)
			}
		}
	}
}

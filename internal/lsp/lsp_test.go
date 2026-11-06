package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

func testdata(t *testing.T, parts ...string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(append([]string{"..", "..", "testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func loadGraph(t *testing.T, fixture string) *project.Graph {
	t.Helper()
	data, err := os.ReadFile(testdata(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	data, err = dataform.ExtractCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	g, err := dataform.ParseCompileJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// rawMsg is any message the server sends.
type rawMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// client drives a Serve running in the background over pipes.
type client struct {
	t         *testing.T
	conn      *conn
	in        chan rawMsg
	notes     []rawMsg
	responses map[string]rawMsg
	nextID    int
	done      chan error
	stop      func()
}

func startServer(t *testing.T, cfg Config) *client {
	t.Helper()
	clientIn, serverOut := io.Pipe()
	serverIn, clientOut := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{t: t, conn: newConn(clientIn, clientOut), in: make(chan rawMsg, 16), done: make(chan error, 1)}
	go func() {
		c.done <- Serve(ctx, serverIn, serverOut, cfg)
		_ = serverOut.Close()
	}()
	go func() {
		defer close(c.in)
		for {
			m, err := c.conn.read()
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					t.Errorf("reading from the server: %v", err)
				}
				return
			}
			c.in <- rawMsg{ID: m.ID, Method: m.Method, Params: m.Params, Result: m.Result, Error: m.Error}
		}
	}()
	c.stop = func() {
		cancel()
		_ = clientOut.Close()
		_ = clientIn.Close()
	}
	t.Cleanup(c.stop)
	return c
}

func (c *client) send(v map[string]any) {
	c.t.Helper()
	v["jsonrpc"] = "2.0"
	if err := c.conn.c.Write(v); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	c.send(map[string]any{"method": method, "params": params})
}

// next returns the next message from the server, failing on a timeout.
func (c *client) next() rawMsg {
	c.t.Helper()
	select {
	case m, ok := <-c.in:
		if !ok {
			c.t.Fatal("server closed the connection")
		}
		return m
	case <-time.After(5 * time.Second):
		c.t.Fatal("timed out waiting for the server")
	}
	return rawMsg{}
}

// request sends a request and returns its response, keeping any
// notifications that arrive first.
func (c *client) request(method string, params any) rawMsg {
	c.t.Helper()
	return c.await(c.start(method, params))
}

// start sends a request and returns its id without waiting for the answer.
func (c *client) start(method string, params any) string {
	c.t.Helper()
	c.nextID++
	c.send(map[string]any{"id": c.nextID, "method": method, "params": params})
	return strconv.Itoa(c.nextID)
}

// await returns the response to request id. Responses to other requests
// that arrive first are kept for their own await.
func (c *client) await(id string) rawMsg {
	c.t.Helper()
	if m, ok := c.responses[id]; ok {
		delete(c.responses, id)
		return m
	}
	for {
		m := c.next()
		switch {
		case m.Method != "":
			c.notes = append(c.notes, m)
		case string(m.ID) == id:
			return m
		default:
			if c.responses == nil {
				c.responses = map[string]rawMsg{}
			}
			c.responses[string(m.ID)] = m
		}
	}
}

// waitNotification returns the first notification with the given method.
func (c *client) waitNotification(method string) rawMsg {
	c.t.Helper()
	for i, m := range c.notes {
		if m.Method == method {
			c.notes = append(c.notes[:i], c.notes[i+1:]...)
			return m
		}
	}
	for {
		m := c.next()
		if m.Method == method {
			return m
		}
		c.notes = append(c.notes, m)
	}
}

// initialize runs the handshake for root, offering the given position
// encodings, and returns the server's capabilities.
func (c *client) initialize(root string, encodings ...string) ServerCapabilities {
	c.t.Helper()
	params := map[string]any{
		"rootUri":      pathURI(root),
		"capabilities": map[string]any{"general": map[string]any{"positionEncodings": encodings}},
	}
	resp := c.request("initialize", params)
	if resp.Error != nil {
		c.t.Fatalf("initialize: %v", resp.Error)
	}
	var res InitializeResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		c.t.Fatal(err)
	}
	c.notify("initialized", map[string]any{})
	return res.Capabilities
}

func (c *client) open(path, text string) {
	c.t.Helper()
	c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(path), "languageId": "sqlx", "version": 1, "text": text},
	})
}

func (c *client) definition(path string, pos Position) []Location {
	c.t.Helper()
	resp := c.request("textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(path)},
		"position":     pos,
	})
	if resp.Error != nil {
		c.t.Fatalf("definition: %v", resp.Error)
	}
	var locs []Location
	if err := json.Unmarshal(resp.Result, &locs); err != nil {
		c.t.Fatalf("decode %s: %v", resp.Result, err)
	}
	return locs
}

func (c *client) shutdown() error {
	c.t.Helper()
	c.request("shutdown", nil)
	c.notify("exit", nil)
	select {
	case err := <-c.done:
		return err
	case <-time.After(5 * time.Second):
		c.t.Fatal("server did not exit")
	}
	return nil
}

func fixtureConfig(t *testing.T) Config {
	g := loadGraph(t, "navigation.json")
	return Config{Compile: func(context.Context, string) (*project.Graph, error) { return g, nil }}
}

func TestServeDefinition(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")

	tests := []struct {
		name      string
		encodings []string
		wantEnc   string
		// The unsaved buffer, its cursor marked with |.
		text     string
		wantFile string
		wantPos  Position
	}{
		{
			name:     "utf-16 by default, multi-byte text before the cursor",
			text:     "SELECT 'é😀' AS x FROM ${ref(\"stg_ev|ents\")}",
			wantEnc:  encodingUTF16,
			wantFile: "definitions/staging/stg_events.sqlx",
		},
		{
			name:      "utf-8 when offered",
			encodings: []string{encodingUTF16, encodingUTF8},
			wantEnc:   encodingUTF8,
			text:      "SELECT 'é😀' AS x FROM ${ref(\"stg_ev|ents\")}",
			wantFile:  "definitions/staging/stg_events.sqlx",
		},
		{
			name:     "target in a js file gets a column",
			text:     "SELECT * FROM ${ref(\"js_v|iew\")}",
			wantEnc:  encodingUTF16,
			wantFile: "definitions/sources.js",
			wantPos:  Position{Line: 2, Character: 9},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := startServer(t, fixtureConfig(t))
			if caps := c.initialize(root, tt.encodings...); caps.PositionEncoding != tt.wantEnc || !caps.DefinitionProvider || caps.TextDocumentSync != syncFull || caps.Experimental == nil || !caps.Experimental.XfActiveFile {
				t.Fatalf("capabilities %+v", caps)
			}
			bar := strings.IndexByte(tt.text, '|')
			text := tt.text[:bar] + tt.text[bar+1:]
			c.open(report, text)
			pos := Position{Character: character([]byte(text), bar, tt.wantEnc == encodingUTF16)}

			got := c.definition(report, pos)
			want := []Location{{URI: pathURI(filepath.Join(root, tt.wantFile)), Range: Range{Start: tt.wantPos, End: tt.wantPos}}}
			if len(got) != 1 || got[0] != want[0] {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			if err := c.shutdown(); err != nil {
				t.Fatalf("exit: %v", err)
			}
		})
	}
}

func TestServeDocumentSymbols(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	file := filepath.Join(root, "definitions", "marts", "report.sqlx")
	c := startServer(t, fixtureConfig(t))
	if caps := c.initialize(root); !caps.DocumentSymbolProvider {
		t.Fatalf("capabilities %+v", caps)
	}
	text := "-- é😀\nWITH base AS (\n  SELECT * FROM ${ref(\"stg_events\")}\n)\nSELECT * FROM base"
	c.open(file, text)
	resp := c.request("textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": pathURI(file)}})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var got []DocumentSymbol
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	want := []DocumentSymbol{
		{
			Name: `ref("stg_events")`, Detail: "ref", Kind: symbolKindVariable,
			Range:          Range{Start: Position{2, 18}, End: Position{2, 35}},
			SelectionRange: Range{Start: Position{2, 23}, End: Position{2, 33}},
		},
		{
			Name: "base", Detail: "cte", Kind: symbolKindStruct,
			Range:          Range{Start: Position{1, 5}, End: Position{3, 1}},
			SelectionRange: Range{Start: Position{1, 5}, End: Position{1, 9}},
		},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("got  %s\nwant %s", gotJSON, wantJSON)
	}

	// A file with nothing to outline is an empty list, not null.
	js := filepath.Join(root, "definitions", "sources.js")
	resp = c.request("textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": pathURI(js)}})
	if string(resp.Result) != "[]" {
		t.Fatalf("js file gave %s", resp.Result)
	}
}

// hoverAt asks for a hover with the cursor at the | in text, opened as file.
func (c *client) hoverAt(file, text string, utf16 bool) string {
	c.t.Helper()
	bar := strings.IndexByte(text, '|')
	text = text[:bar] + text[bar+1:]
	c.open(file, text)
	id := c.start("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(file)},
		"position":     positionAt([]byte(text), bar, utf16),
	})
	return id
}

func decodeHover(t *testing.T, m rawMsg) *Hover {
	t.Helper()
	if m.Error != nil {
		t.Fatal(m.Error)
	}
	var h *Hover
	if err := json.Unmarshal(m.Result, &h); err != nil {
		t.Fatalf("decode %s: %v", m.Result, err)
	}
	return h
}

func TestServeHover(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	var asked []string
	cfg := fixtureConfig(t)
	cfg.Tables = func(_ context.Context, project, dataset, table string) (*bigquery.Table, error) {
		asked = append(asked, project+"."+dataset+"."+table)
		if table == "orders" {
			return nil, errors.New("Access Denied (HTTP 403)")
		}
		return &bigquery.Table{Type: "VIEW", Location: "EU", Schema: []bigquery.Field{{Name: "user_id", Type: "STRING"}}}, nil
	}
	c := startServer(t, cfg)
	if caps := c.initialize(root); !caps.HoverProvider {
		t.Fatalf("capabilities %+v", caps)
	}

	tests := []struct {
		name      string
		text      string
		wantAsk   string
		contains  []string
		wantRange Range
	}{
		{
			name:      "ref",
			text:      "SELECT 'é' FROM ${ref(\"stg_e|vents\")}",
			wantAsk:   "demo-project.analytics.dev_stg_events",
			contains:  []string{"#### [demo-project.analytics.dev_stg_events](", "**Type:** view", "**Location:** EU", "| user_id | STRING |"},
			wantRange: Range{Start: Position{0, 18}, End: Position{0, 35}},
		},
		{
			name:     "raw id BigQuery refuses",
			text:     "SELECT * FROM other-proj.sales.ord|ers",
			wantAsk:  "other-proj.sales.orders",
			contains: []string{"#### [other-proj.sales.orders](", "⚠ **BigQuery metadata unavailable:** Access Denied (HTTP 403)"},
		},
		{
			name:     "self asks nothing",
			text:     "SELECT 1 -- ${se|lf()}",
			contains: []string{"#### [demo-project.analytics.dev_report]("},
		},
		{
			name:      "a config key",
			text:      "config {\n  type: \"view\",\n  bigquery: { partit|ionBy: \"d\" },\n  protected: true\n}",
			contains:  []string{"#### `bigquery.partitionBy: string`", "PARTITION BY", "For: table, view, incremental"},
			wantRange: Range{Start: Position{2, 14}, End: Position{2, 25}},
		},
		{
			name:     "a config key its type doesn't read",
			text:     "config {\n  type: \"view\",\n  prot|ected: true\n}",
			contains: []string{"#### `protected: bool`", "For: incremental", "⚠ a view doesn't read it"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked = nil
			id := c.hoverAt(report, tt.text, true)
			h := decodeHover(t, c.await(id))
			if h == nil || h.Contents.Kind != "markdown" {
				t.Fatalf("got %+v", h)
			}
			for _, want := range tt.contains {
				if !strings.Contains(h.Contents.Value, want) {
					t.Errorf("card lacks %q:\n%s", want, h.Contents.Value)
				}
			}
			if tt.wantAsk != "" && (len(asked) != 1 || asked[0] != tt.wantAsk) || tt.wantAsk == "" && len(asked) != 0 {
				t.Errorf("asked BigQuery for %v, want %q", asked, tt.wantAsk)
			}
			if tt.wantRange != (Range{}) && (h.Range == nil || *h.Range != tt.wantRange) {
				t.Errorf("range %+v, want %+v", h.Range, tt.wantRange)
			}
		})
	}

	id := c.hoverAt(report, "SELECT us|er_id FROM t", true)
	if m := c.await(id); string(m.Result) != "null" {
		t.Errorf("hover on a column gave %s", m.Result)
	}
}

// TestServeHoverOperation looks up an operation's table only when it has
// hasOutput, and says why the other has no schema.
func TestServeHoverOperation(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	g := loadGraph(t, "navigation.json")
	for name, hasOutput := range map[string]bool{"setup": true, "cleanup": false} {
		target := project.Target{Database: "demo-project", Schema: "analytics", Name: name}
		g.Actions[target.ID()] = &project.Action{ID: target.ID(), Target: target, CanonicalTarget: target,
			Kind: project.KindOperation, FileName: "definitions/" + name + ".sqlx", HasOutput: hasOutput}
	}
	var asked []string
	cfg := Config{Compile: func(context.Context, string) (*project.Graph, error) { return g, nil }}
	cfg.Tables = func(_ context.Context, project, dataset, table string) (*bigquery.Table, error) {
		asked = append(asked, project+"."+dataset+"."+table)
		return &bigquery.Table{Type: "TABLE", Schema: []bigquery.Field{{Name: "run_id", Type: "INT64"}}}, nil
	}
	c := startServer(t, cfg)
	c.initialize(root)

	tests := []struct {
		name, text, wantAsk, want string
	}{
		{"hasOutput", `SELECT * FROM ${ref("set|up")}`, "demo-project.analytics.setup", "| run_id | INT64 |"},
		{"no hasOutput", `SELECT * FROM ${ref("clean|up")}`, "", "_no schema: this operation has no output table (set `hasOutput: true` to give it one)_"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked = nil
			h := decodeHover(t, c.await(c.hoverAt(report, tt.text, true)))
			if h == nil || !strings.Contains(h.Contents.Value, tt.want) || !strings.Contains(h.Contents.Value, "**Type:** operation") {
				t.Fatalf("card lacks %q:\n%+v", tt.want, h)
			}
			if tt.wantAsk != "" && (len(asked) != 1 || asked[0] != tt.wantAsk) || tt.wantAsk == "" && len(asked) != 0 {
				t.Errorf("asked BigQuery for %v, want %q", asked, tt.wantAsk)
			}
		})
	}
}

func TestServeHoverTimesOutWithoutBlocking(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	release := make(chan struct{})
	cfg := fixtureConfig(t)
	cfg.HoverTimeout = 300 * time.Millisecond
	cfg.Tables = func(ctx context.Context, _, _, _ string) (*bigquery.Table, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return nil, errors.New("released")
		}
	}
	c := startServer(t, cfg)
	c.initialize(root)
	text := "SELECT * FROM ${ref(\"stg_events\")}"
	hoverID := c.hoverAt(report, strings.Replace(text, "stg_", "stg_|", 1), true)

	// A definition asked for while BigQuery hangs is answered first.
	defID := c.start("textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(report)},
		"position":     Position{Line: 0, Character: 25},
	})
	for {
		m := c.next()
		if m.Method != "" {
			continue
		}
		if string(m.ID) != defID {
			t.Fatalf("got the response to %s before the definition", m.ID)
		}
		break
	}
	h := decodeHover(t, c.await(hoverID))
	if h == nil || !strings.Contains(h.Contents.Value, "⚠ **BigQuery metadata unavailable:** timed out after 300ms") {
		t.Fatalf("got %+v", h)
	}
	close(release)
}

func TestServeNoLocations(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	c := startServer(t, fixtureConfig(t))
	c.initialize(root)
	resp := c.request("textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(filepath.Join(root, "definitions", "marts", "report.sqlx"))},
		"position":     Position{Line: 0, Character: 0},
	})
	if resp.Error != nil || string(resp.Result) != "null" {
		t.Fatalf("got result %s, error %v; want null", resp.Result, resp.Error)
	}
}

func TestServeWithoutDataform(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	c := startServer(t, Config{
		Compile:   func(context.Context, string) (*project.Graph, error) { t.Error("compiled"); return nil, nil },
		NoCompile: func(string) error { return errors.New("could not find \"dataform\" on PATH") },
	})
	c.initialize(root)
	m := c.waitNotification("window/showMessage")
	var p ShowMessageParams
	if err := json.Unmarshal(m.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != messageWarning || !strings.Contains(p.Message, "on PATH") {
		t.Fatalf("got %+v", p)
	}
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	c.open(report, `SELECT * FROM ${ref("stg_events")}`)
	if got := c.definition(report, Position{Character: 22}); len(got) != 0 {
		t.Fatalf("got %+v, want nothing without a compile", got)
	}
}

// TestServeDbt serves a dbt project: jumps through ref(), source() and
// macros, hovers, completion and an outline (TestServeDbtDryRunDiagnostics
// has its dry runs).
func TestServeDbt(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "dbt"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(testdata(t, "examples", "dbt-v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := (&dbt.Backend{}).Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	c := startServer(t, Config{
		Compile: func(context.Context, string) (*project.Graph, error) { return g, nil },
		DryRun: func(context.Context, string, string, string) (*bigquery.DryRun, error) {
			return &bigquery.DryRun{Bytes: 1 << 20, Accuracy: bigquery.AccuracyPrecise}, nil
		},
		Dbt: func(r string) bool { return r == root },
	})
	caps := c.initialize(root)
	if !caps.CodeActionProvider || caps.CompletionProvider == nil || !slices.Contains(caps.CompletionProvider.TriggerCharacters, "{") || !caps.Experimental.XfDbt {
		t.Fatalf("capabilities %+v", caps)
	}

	model := filepath.Join(root, "models", "staging", "stg_payments.sql")
	text := "select {{ cents_to_dollars('amount') }}\nfrom {{ source('raw', 'payments') }}"
	c.open(model, text)
	if got := c.definition(model, Position{Line: 0, Character: 12}); len(got) != 1 || got[0].URI != pathURI(filepath.Join(root, "macros", "cents_to_dollars.sql")) || got[0].Range.Start != (Position{0, 9}) {
		t.Errorf("macro definition: got %+v", got)
	}
	if got := c.definition(model, Position{Line: 1, Character: 28}); len(got) != 1 || got[0].URI != pathURI(filepath.Join(root, "models", "staging", "_sources.yml")) || got[0].Range.Start.Line == 0 {
		t.Errorf("source definition: got %+v", got)
	}

	h := decodeHover(t, c.await(c.hoverAt(model, "select {{ cents_to_do|llars('amount') }}", true)))
	if h == nil || !strings.Contains(h.Contents.Value, "#### `cents_to_dollars(column)`") || !strings.Contains(h.Contents.Value, "xf_example · `macros/cents_to_dollars.sql`") {
		t.Errorf("macro hover: got %+v", h)
	}
	h = decodeHover(t, c.await(c.hoverAt(model, "select * from {{ ref('stg_or|ders') }}", true)))
	if h == nil || !strings.Contains(h.Contents.Value, "stg_orders") {
		t.Errorf("ref hover: got %+v", h)
	}

	c.open(model, "select * from {{ ref('stg_o")
	resp := c.request("textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": pathURI(model)},
		"position":     Position{Line: 0, Character: 27},
	})
	var list CompletionList
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list.Items, func(it CompletionItem) bool { return it.Label == "stg_orders" })
	if i < 0 || list.Items[i].TextEdit.Range.Start != (Position{0, 22}) || list.Items[i].TextEdit.NewText != "stg_orders" {
		t.Errorf("completion: got %+v", list.Items)
	}

	audit := filepath.Join(root, "macros", "audit.sql")
	resp = c.request("textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": pathURI(audit)}})
	var syms []DocumentSymbol
	if err := json.Unmarshal(resp.Result, &syms); err != nil {
		t.Fatal(err)
	}
	if len(syms) != 3 || syms[0].Kind != symbolKindFunction || syms[0].Name != "create_cents_to_dollars_udf()" {
		t.Errorf("outline: got %+v", syms)
	}
	if err := c.shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestServeCompileFailureWarnsOnce(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	changes := make(chan struct{})
	compiles := make(chan struct{}, 4)
	c := startServer(t, Config{
		Compile: func(context.Context, string) (*project.Graph, error) {
			compiles <- struct{}{}
			return nil, errors.New("boom")
		},
		Watch: func(context.Context, string) (<-chan struct{}, error) { return changes, nil },
	})
	c.initialize(root)
	c.waitNotification("window/showMessage")
	<-compiles
	changes <- struct{}{}
	<-compiles
	// The third compile starts only after the second has sent anything it
	// was going to, and a request round trip flushes that to us.
	changes <- struct{}{}
	<-compiles
	c.request("unknown/request", nil)
	for _, m := range c.notes {
		if m.Method == "window/showMessage" {
			t.Fatalf("warned again: %s", m.Params)
		}
	}
}

func TestServeProtocolErrors(t *testing.T) {
	c := startServer(t, fixtureConfig(t))
	c.initialize(testdata(t, "projects", "navigation"))
	if resp := c.request("textDocument/rename", map[string]any{}); resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("unknown method: got %+v", resp)
	}
	c.request("shutdown", nil)
	if resp := c.request("textDocument/definition", map[string]any{}); resp.Error == nil || resp.Error.Code != codeInvalidRequest {
		t.Errorf("request after shutdown: got %+v", resp)
	}
}

func TestServeExitWithoutShutdown(t *testing.T) {
	c := startServer(t, fixtureConfig(t))
	c.notify("exit", nil)
	select {
	case err := <-c.done:
		if !errors.Is(err, errExitWithoutShutdown) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit")
	}
}

func TestPositions(t *testing.T) {
	text := []byte("ab\né😀x\n")
	tests := []struct {
		pos        Position
		utf16      bool
		wantOffset int
	}{
		{Position{0, 1}, false, 1},
		{Position{1, 0}, true, 3},
		{Position{1, 1}, true, 5},  // after é (1 unit, 2 bytes)
		{Position{1, 3}, true, 9},  // after 😀 (2 units, 4 bytes)
		{Position{1, 6}, false, 9}, // utf-8 counts bytes
		{Position{1, 99}, true, 10},
		{Position{9, 0}, false, len(text)},
	}
	for _, tt := range tests {
		if got := byteOffset(text, tt.pos, tt.utf16); got != tt.wantOffset {
			t.Errorf("byteOffset(%+v, utf16=%v) = %d, want %d", tt.pos, tt.utf16, got, tt.wantOffset)
		}
	}
	line := lineAt(text, 1)
	if string(line) != "é😀x" {
		t.Fatalf("lineAt = %q", line)
	}
	if got := character(line, 6, true); got != 3 {
		t.Errorf("character utf-16 = %d, want 3", got)
	}
	if got := character(line, 6, false); got != 6 {
		t.Errorf("character utf-8 = %d, want 6", got)
	}
	for _, tt := range []struct {
		offset int
		utf16  bool
		want   Position
	}{
		{0, true, Position{0, 0}},
		{3, true, Position{1, 0}},
		{9, true, Position{1, 3}},
		{9, false, Position{1, 6}},
		{99, false, Position{2, 0}},
	} {
		if got := positionAt(text, tt.offset, tt.utf16); got != tt.want {
			t.Errorf("positionAt(%d, utf16=%v) = %+v, want %+v", tt.offset, tt.utf16, got, tt.want)
		}
	}
}

func TestURIs(t *testing.T) {
	defer func(g string) { goos = g }(goos)
	for _, tt := range []struct {
		goos, path, uri string
		// also: other URIs clients send for path
		also []string
	}{
		{"linux", "/tmp/my project/a#b.sqlx", "file:///tmp/my%20project/a%23b.sqlx", nil},
		{"windows", `C:\src\my project\a#b.sqlx`, "file:///C:/src/my%20project/a%23b.sqlx",
			[]string{"file:///c%3A/src/my%20project/a%23b.sqlx", "file:///c:/src/my%20project/a%23b.sqlx"}},
		{"windows", `\\server\share\a.sqlx`, "file://server/share/a.sqlx", nil},
	} {
		goos = tt.goos
		if got := pathURI(tt.path); got != tt.uri {
			t.Errorf("%s: pathURI(%q) = %q, want %q", tt.goos, tt.path, got, tt.uri)
		}
		for _, uri := range append([]string{tt.uri}, tt.also...) {
			if got := uriPath(uri); got != tt.path {
				t.Errorf("%s: uriPath(%q) = %q, want %q", tt.goos, uri, got, tt.path)
			}
		}
	}
	if got := uriPath("untitled:Untitled-1"); got != "" {
		t.Errorf("non-file URI gave %q", got)
	}
}

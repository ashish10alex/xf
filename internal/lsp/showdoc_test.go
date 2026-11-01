package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/internal/testutil"
)

// sharedServer runs an xf serve for root, and returns how to connect to
// it.
func sharedServer(t *testing.T, root string, g *project.Graph) func(ctx context.Context) (*remote.Client, error) {
	t.Helper()
	path := testutil.SocketPath(t)
	ln, err := remote.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	eng := engine.New(engine.Config{Compile: func(context.Context) (*project.Graph, error) { return g, nil }})
	_ = eng.Start(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = remote.Serve(ctx, ln, remote.ServerConfig{Root: root, Engine: eng, Grace: time.Minute})
	}()
	t.Cleanup(func() { cancel(); <-done })
	return func(ctx context.Context) (*remote.Client, error) {
		return remote.Connect(ctx, remote.ClientConfig{
			Dial:  remote.Dialer(path, func() error { return errors.New("no second server") }),
			Parse: dataform.ParseCompileJSON,
		})
	}
}

// editor starts an xf lsp attached to the shared server, as an editor
// that can (or can't) open files the server asks it to.
func editor(t *testing.T, root string, dial func(context.Context) (*remote.Client, error), showDocs bool) *client {
	t.Helper()
	c := startServer(t, Config{Connect: func(ctx context.Context, _ string) (Engine, error) {
		cl, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		t.Cleanup(cl.Close)
		return cl, nil
	}})
	resp := c.request("initialize", map[string]any{
		"rootUri":      pathURI(root),
		"capabilities": map[string]any{"window": map[string]any{"showDocument": map[string]any{"support": showDocs}}},
	})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	c.notify("initialized", map[string]any{})
	return c
}

// activeIn has editor c say file is active, and waits until tui hears it.
func activeIn(t *testing.T, c *client, tui *remote.Client, file string) {
	t.Helper()
	c.notify("xf/activeFile", map[string]any{"event": "BufEnter", "file": file})
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-tui.Editor():
			return
		case <-timeout:
			t.Fatal("the active file didn't reach the TUI")
		}
	}
}

// The TUI's o asks the editor used last to open the file, through its xf
// lsp's window/showDocument; an editor that can't is never asked.
func TestShowDocumentReachesTheLastActiveEditor(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	events := filepath.Join(root, "definitions", "staging", "stg_events.sqlx")
	dial := sharedServer(t, root, loadGraph(t, "navigation.json"))
	tui, err := dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tui.Close()
	first, second := editor(t, root, dial, true), editor(t, root, dial, true)

	activeIn(t, first, tui, report)
	activeIn(t, second, tui, report)
	shown := make(chan error, 1)
	go func() { shown <- tui.ShowDocument(context.Background(), events, 4) }()
	req := second.waitNotification("window/showDocument")
	var p ShowDocumentParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.URI != pathURI(events) || !p.TakeFocus || p.Selection == nil || p.Selection.Start.Line != 4 {
		t.Errorf("asked to show %+v", p)
	}
	second.send(map[string]any{"id": req.ID, "result": map[string]any{"success": true}})
	if err := <-shown; err != nil {
		t.Fatalf("show document: %v", err)
	}

	// The editor says it couldn't.
	activeIn(t, first, tui, events)
	go func() { shown <- tui.ShowDocument(context.Background(), report, 0) }()
	req = first.waitNotification("window/showDocument")
	first.send(map[string]any{"id": req.ID, "result": map[string]any{"success": false}})
	if err := <-shown; err == nil || errors.Is(err, remote.ErrNoEditor) {
		t.Errorf("an editor that failed to open the file: %v", err)
	}

	// An editor without window/showDocument.
	plain := editor(t, root, dial, false)
	activeIn(t, plain, tui, report)
	if err := tui.ShowDocument(context.Background(), report, 0); !errors.Is(err, remote.ErrNoEditor) {
		t.Errorf("an editor that can't show documents: %v, want ErrNoEditor", err)
	}
}

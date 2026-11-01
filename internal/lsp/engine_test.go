package lsp

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/project"
)

// sharedEngine stands in for a remote client: an engine that records the
// active files it is told about.
type sharedEngine struct {
	*engine.Engine
	files chan [2]string
}

func (e *sharedEngine) ActiveFile(event, file string) { e.files <- [2]string{event, file} }

func TestServeAttachesToSharedEngine(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	g := loadGraph(t, "navigation.json")
	shared := &sharedEngine{files: make(chan [2]string, 4)}
	var gotRoot string
	cfg := Config{
		Connect: func(ctx context.Context, r string) (Engine, error) {
			gotRoot = r
			shared.Engine = engine.New(engine.Config{Compile: func(context.Context) (*project.Graph, error) { return g, nil }})
			_ = shared.Start(ctx)
			return shared, nil
		},
		Compile: func(context.Context, string) (*project.Graph, error) {
			t.Error("compiled its own engine although attached")
			return g, nil
		},
	}
	c := startServer(t, cfg)
	c.initialize(root)
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	c.open(report, `SELECT * FROM ${ref("stg_events")}`)
	if got := c.definition(report, Position{Character: 22}); len(got) != 1 {
		t.Fatalf("definition through the shared engine: %+v", got)
	}
	if gotRoot != root {
		t.Errorf("connected for %q, want %q", gotRoot, root)
	}

	c.notify("xf/activeFile", map[string]any{"event": "BufEnter", "file": report})
	select {
	case got := <-shared.files:
		if got != [2]string{"BufEnter", report} {
			t.Errorf("got %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the active file was not passed on")
	}
}

func TestServeFallsBackToOwnEngine(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	var mu sync.Mutex
	compiled := false
	cfg := fixtureConfig(t)
	compile := cfg.Compile
	cfg.Compile = func(ctx context.Context, r string) (*project.Graph, error) {
		mu.Lock()
		compiled = true
		mu.Unlock()
		return compile(ctx, r)
	}
	cfg.Connect = func(context.Context, string) (Engine, error) { return nil, errors.New("no socket") }
	c := startServer(t, cfg)
	c.initialize(root)
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	c.open(report, `SELECT * FROM ${ref("stg_events")}`)
	if got := c.definition(report, Position{Character: 22}); len(got) != 1 {
		t.Fatalf("definition: %+v", got)
	}
	// Without a shared engine, the active file goes nowhere, quietly.
	c.notify("xf/activeFile", map[string]any{"event": "BufEnter", "file": report})
	c.request("unknown/flush", nil)
	mu.Lock()
	defer mu.Unlock()
	if !compiled {
		t.Error("did not compile with its own engine")
	}
}

// The first file an editor opens is usually made active before the server
// has attached to the engine (attaching can take seconds, starting xf
// serve): it's passed on once it has, not dropped.
func TestServePassesOnAnActiveFileFromBeforeTheEngine(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	g := loadGraph(t, "navigation.json")
	shared := &sharedEngine{files: make(chan [2]string, 4)}
	attach := make(chan struct{})
	cfg := Config{
		Connect: func(ctx context.Context, r string) (Engine, error) {
			<-attach // a slow start
			shared.Engine = engine.New(engine.Config{Compile: func(context.Context) (*project.Graph, error) { return g, nil }})
			_ = shared.Start(ctx)
			return shared, nil
		},
	}
	c := startServer(t, cfg)
	c.initialize(root)
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	c.notify("xf/activeFile", map[string]any{"event": "BufEnter", "file": report})
	time.Sleep(50 * time.Millisecond) // let the server read it while unattached
	close(attach)
	select {
	case got := <-shared.files:
		if got != [2]string{"BufEnter", report} {
			t.Errorf("got %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the active file from before the engine was up was dropped")
	}
}

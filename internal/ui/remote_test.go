package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/internal/testutil"
)

// TestProgramOverSocket runs the TUI against a real xf serve on a socket,
// with a second client standing in for the editor's language server: its
// active file reaches the TUI through the server.
func TestProgramOverSocket(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "projects", "basic"))
	if err != nil {
		t.Fatal(err)
	}
	path := testutil.SocketPath(t)

	ln, err := remote.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	eng := engine.New(engine.Config{Compile: fc.compile})
	_ = eng.Start(ctx)
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = remote.Serve(ctx, ln, remote.ServerConfig{Root: root, Engine: eng, Grace: time.Minute})
	}()
	t.Cleanup(func() { cancel(); <-served })

	dial := remote.Dialer(path, func() error { t.Error("started a server"); return nil })
	tui, err := remote.Connect(ctx, remote.ClientConfig{Dial: dial, Parse: dataform.ParseCompileJSON})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tui.Close)
	editor, err := remote.Connect(ctx, remote.ClientConfig{Dial: dial, Parse: dataform.ParseCompileJSON})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(editor.Close)

	m := New(Deps{Root: root, Engine: tui, DryRun: tui.DryRun, Editor: tui.Editor()})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 40))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), "daily_orders")
	}, teatest.WithDuration(5*time.Second))

	editor.ActiveFile("BufEnter", filepath.Join(root, "definitions", "weekly_revenue.sqlx"))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), "weekly_revenue.sqlx")
	}, teatest.WithDuration(5*time.Second))
	tm.Send(keyMsg("esc")) // the palette the compile opened
	tm.Send(keyMsg("q"))
	tm.Send(keyMsg("q")) // q asks, q again quits
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(Model)
	if final.selected == nil || final.selected.ID != "demo-project.analytics.weekly_revenue" {
		t.Errorf("selected = %v", final.selected)
	}
}

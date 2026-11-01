package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

func TestCurrentSavedCompileSkipsTheStartupCompile(t *testing.T) {
	saved, fresh := loadGraph(t, "basic.json"), loadGraph(t, "incremental.json")
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	changes := make(chan struct{})
	var calls atomic.Int32
	e := New(Config{
		Compile: func(context.Context) (*project.Graph, error) { calls.Add(1); return fresh, nil },
		Watch:   func(context.Context) (<-chan struct{}, error) { return changes, nil },
		Saved:   func() *Saved { return &Saved{Graph: saved, CompiledAt: at, Took: 3 * time.Second} },
	})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := e.State()
	if s.Graph != saved || !s.Saved || s.Outdated != "" || s.Compiling || s.Compiles != 1 || !s.CompiledAt.Equal(at) || s.Took != 3*time.Second {
		t.Fatalf("the saved compile should be in use, as a finished compile: %+v", s)
	}
	select {
	case <-e.FirstCompile():
	default:
		t.Fatal("FirstCompile should be closed: the saved graph matches the files")
	}

	ch, cancel := e.Subscribe()
	defer cancel()
	changes <- struct{}{}
	s = next(t, ch, compiled(2))
	if s.Graph != fresh || s.Saved || calls.Load() != 1 {
		t.Fatalf("a file change should compile as usual: %+v, %d compiles", s, calls.Load())
	}
}

func TestOutdatedSavedCompileShowsUntilTheStartupCompileEnds(t *testing.T) {
	saved, fresh := loadGraph(t, "basic.json"), loadGraph(t, "incremental.json")
	release := make(chan struct{})
	e := New(Config{
		Compile: func(context.Context) (*project.Graph, error) { <-release; return fresh, nil },
		Saved:   func() *Saved { return &Saved{Graph: saved, Outdated: "project files changed"} },
	})
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := next(t, ch, func(s State) bool { return s.Graph != nil })
	if s.Graph != saved || !s.Saved || s.Outdated != "project files changed" || !s.Compiling || s.Compiles != 1 {
		t.Fatalf("the outdated saved compile should show while compiling: %+v", s)
	}
	select {
	case <-e.FirstCompile():
		t.Fatal("FirstCompile closed before the graph matches the files")
	default:
	}

	close(release)
	s = next(t, ch, compiled(2))
	if s.Graph != fresh || s.Saved || s.Outdated != "" || s.Stale {
		t.Fatalf("the startup compile should replace it: %+v", s)
	}
	<-e.FirstCompile()
}

func TestOutdatedSavedCompileStaysStaleWhenTheProjectIsBroken(t *testing.T) {
	saved, broken := loadGraph(t, "basic.json"), loadGraph(t, "compile_error.json")
	for name, compile := range map[string]func(context.Context) (*project.Graph, error){
		"errors": func(context.Context) (*project.Graph, error) { return broken, nil },
		"failed": func(context.Context) (*project.Graph, error) { return nil, errors.New("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			e := New(Config{
				Compile: compile,
				Saved:   func() *Saved { return &Saved{Graph: saved, Outdated: "the dataform CLI changed"} },
			})
			ch, cancel := e.Subscribe()
			defer cancel()
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			if err := e.Start(ctx); err != nil {
				t.Fatal(err)
			}
			s := next(t, ch, compiled(2))
			if s.Graph != saved || !s.Saved || !s.Stale || s.Outdated != "" {
				t.Fatalf("the saved graph should stay, marked stale: %+v", s)
			}
			<-e.FirstCompile()
		})
	}
}

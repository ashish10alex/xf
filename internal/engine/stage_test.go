package engine

import (
	"context"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// stagedGraph is a graph of actions a, b and c in the SQL states given, in
// that order.
func stagedGraph(states ...project.SQLState) *project.Graph {
	var actions []*project.Action
	for i, st := range states {
		t := project.Target{Database: "d", Schema: "s", Name: string(rune('a' + i))}
		actions = append(actions, &project.Action{ID: t.ID(), Target: t, CanonicalTarget: t, Query: "select 1", SQL: st})
	}
	return project.NewGraph(actions)
}

// focusCall is one call of Config.Focus, which returns what is sent on
// reply, or its context's error once that is done.
type focusCall struct {
	id    project.ActionID
	base  *project.Graph
	ctx   context.Context
	reply chan *project.Graph
}

// stagedConfig is a Config whose Stage returns staged, whose Compile
// returns what is sent on compile and whose Focus calls arrive on focus.
func stagedConfig(staged *project.Graph) (cfg Config, compile chan *project.Graph, focus chan focusCall) {
	compile, focus = make(chan *project.Graph), make(chan focusCall, 8)
	cfg = Config{
		Stage: func(context.Context, *project.Graph) (*project.Graph, bool, error) { return staged, false, nil },
		Compile: func(ctx context.Context) (*project.Graph, error) {
			select {
			case g := <-compile:
				return g, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		Focus: func(ctx context.Context, base *project.Graph, id project.ActionID) (*project.Graph, error) {
			c := focusCall{id, base, ctx, make(chan *project.Graph, 1)}
			focus <- c
			select {
			case g := <-c.reply:
				return g, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	return cfg, compile, focus
}

func recvFocus(t *testing.T, focus chan focusCall) focusCall {
	t.Helper()
	select {
	case c := <-focus:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no focused compile started")
		return focusCall{}
	}
}

func TestStagedGraphShowsBeforeTheCompile(t *testing.T) {
	staged := stagedGraph(project.SQLRaw, project.SQLRaw)
	focused := stagedGraph(project.SQLCompiled, project.SQLRaw)
	whole := stagedGraph(project.SQLCompiled, project.SQLCompiled)
	cfg, compile, focus := stagedConfig(staged)
	cfg.Suggest = func(context.Context, *project.Graph) project.ActionID { return "d.s.a" }
	e := New(cfg)
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}

	s := next(t, ch, func(s State) bool { return s.Graph != nil })
	if s.Graph != staged || !s.Partial || !s.Compiling || s.Compiles != 0 || s.Focus != "d.s.a" {
		t.Fatalf("the staged graph, with the suggested action being compiled first: %+v", s)
	}
	select {
	case <-e.FirstCompile():
		t.Fatal("FirstCompile closed on a staged graph")
	default:
	}
	c := recvFocus(t, focus)
	if c.id != "d.s.a" || c.base != staged {
		t.Fatalf("focused compile of %s on %p, want d.s.a on the staged graph", c.id, c.base)
	}
	c.reply <- focused
	if s := next(t, ch, func(s State) bool { return s.Graph == focused }); !s.Partial || s.Focus != "" || s.Compiles != 0 {
		t.Fatalf("after the focused compile: %+v", s)
	}

	compile <- whole
	if s := next(t, ch, compiled(1)); s.Graph != whole || s.Partial || s.Focus != "" {
		t.Fatalf("after the compile: %+v", s)
	}
	select {
	case <-e.FirstCompile():
	case <-time.After(5 * time.Second):
		t.Fatal("FirstCompile not closed after the compile")
	}
}

func TestFocusLatestWins(t *testing.T) {
	staged := stagedGraph(project.SQLRaw, project.SQLRaw, project.SQLCompiled)
	cfg, compile, focus := stagedConfig(staged)
	e := New(cfg)
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	if s := next(t, ch, func(s State) bool { return s.Partial }); s.Focus != "" {
		t.Fatalf("nothing asked for and nothing suggested, yet focus = %q", s.Focus)
	}

	e.Focus("d.s.a")
	a := recvFocus(t, focus)
	next(t, ch, func(s State) bool { return s.Focus == "d.s.a" })
	e.Focus("d.s.b")
	b := recvFocus(t, focus)
	if a.id != "d.s.a" || b.id != "d.s.b" {
		t.Fatalf("focused %s then %s", a.id, b.id)
	}
	select {
	case <-a.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the first focused compile wasn't dropped for the second")
	}
	next(t, ch, func(s State) bool { return s.Focus == "d.s.b" })

	// An action whose SQL is compiled already starts nothing, and neither
	// does the one in flight asked for again.
	e.Focus("d.s.c")
	e.Focus("d.s.b")
	after := stagedGraph(project.SQLRaw, project.SQLCompiled, project.SQLCompiled)
	b.reply <- after
	next(t, ch, func(s State) bool { return s.Graph == after })
	select {
	case c := <-focus:
		t.Fatalf("an unwanted focused compile of %s", c.id)
	case <-time.After(50 * time.Millisecond):
	}

	// The next staged graph starts with the action asked for last, when it
	// is yet to be compiled.
	compile <- stagedGraph(project.SQLCompiled, project.SQLCompiled, project.SQLCompiled)
	next(t, ch, compiled(1))
	e.Focus("d.s.a")
	e.Recompile("")
	if s := next(t, ch, func(s State) bool { return s.Partial }); s.Focus != "d.s.a" {
		t.Fatalf("the next staged graph's focus = %q, want d.s.a", s.Focus)
	}
}

func TestFocusAfterSaveIsTheSavedFile(t *testing.T) {
	// b's template is new (the file just saved); a keeps its old SQL.
	staged := stagedGraph(project.SQLOutdated, project.SQLRaw)
	cfg, _, focus := stagedConfig(staged)
	cfg.Suggest = func(context.Context, *project.Graph) project.ActionID { return "d.s.b" }
	changed := make(chan struct{})
	cfg.Watch = func(context.Context) (<-chan struct{}, error) { return changed, nil }
	e := New(cfg)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	e.Focus("d.s.a")
	if c := recvFocus(t, focus); c.id != "d.s.b" && c.id != "d.s.a" {
		t.Fatalf("focused %s", c.id)
	}
	changed <- struct{}{}
	for {
		c := recvFocus(t, focus)
		if c.ctx.Err() != nil {
			continue // of the compile the save superseded
		}
		if c.id != "d.s.b" {
			t.Fatalf("after a save the focused compile is of %s, want the saved d.s.b over the asked-for d.s.a", c.id)
		}
		break
	}
}

func TestFocusedErrorsShowAndFinalStageSkipsCompile(t *testing.T) {
	staged := stagedGraph(project.SQLRaw)
	cfg, _, focus := stagedConfig(staged)
	e := New(cfg)
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	next(t, ch, func(s State) bool { return s.Partial })
	e.Focus("d.s.a")
	c := recvFocus(t, focus)
	c.reply <- &project.Graph{Errors: []project.CompilationError{{FileName: "models/a.sql", Message: "boom"}}}
	if s := next(t, ch, func(s State) bool { return len(s.Errors) > 0 }); s.Graph != staged || !s.Partial {
		t.Fatalf("a focused compile's errors should show beside the staged graph: %+v", s)
	}

	final := stagedGraph(project.SQLCompiled)
	final.Notice = "parsed only"
	e2 := New(Config{
		Stage: func(context.Context, *project.Graph) (*project.Graph, bool, error) { return final, true, nil },
		Compile: func(context.Context) (*project.Graph, error) {
			t.Error("Compile ran after a final stage")
			return nil, nil
		},
	})
	ch2, cancel2 := e2.Subscribe()
	defer cancel2()
	_ = e2.Start(ctx)
	if s := next(t, ch2, compiled(1)); s.Graph != final || s.Partial {
		t.Fatalf("a final stage is the compile's result: %+v", s)
	}
}

func TestChangedActionsWaitForTheWholeCompile(t *testing.T) {
	whole := stagedGraph(project.SQLCompiled)
	cfg, compile, _ := stagedConfig(stagedGraph(project.SQLOutdated))
	cfg.Changes = &fakeChanges{}
	e := New(cfg)
	ch, cancel := e.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_ = e.Start(ctx)
	compile <- whole
	next(t, ch, compiled(1))
	e.Recompile("")
	next(t, ch, func(s State) bool { return s.Partial })

	got := make(chan *project.Graph, 1)
	go func() {
		_, g, _ := e.changedActions(ctx, changes.Request{DefaultBranch: "main", Head: changes.HeadWorktree})
		got <- g
	}()
	select {
	case g := <-got:
		t.Fatalf("Changed Actions were worked out from a staged graph: %p", g)
	case <-time.After(50 * time.Millisecond):
	}
	again := stagedGraph(project.SQLCompiled)
	compile <- again
	select {
	case g := <-got:
		if g != again {
			t.Fatalf("worked out from %p, want the finished compile", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Changed Actions never came")
	}
}

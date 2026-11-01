package web

import (
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

// compilingEngine is an engine whose first compile ends when first is
// closed, as the remote client says.
type compilingEngine struct {
	*fakeEngine
	first chan struct{}
	g     *project.Graph
}

func (e *compilingEngine) FirstCompile() <-chan struct{} { return e.first }
func (e *compilingEngine) State() engine.State {
	select {
	case <-e.first:
		return engine.State{Graph: e.g, Compiles: 1}
	default:
		return engine.State{Compiling: true}
	}
}

// targetGraph is a project compiled with a table prefix and suffixes.
func targetGraph() *project.Graph {
	add := func(g *project.Graph, kind project.ActionKind, file string, canon, t project.Target) {
		a := &project.Action{ID: t.ID(), Kind: kind, FileName: file, Target: t, CanonicalTarget: canon}
		g.Actions[a.ID] = a
	}
	g := &project.Graph{Actions: map[project.ActionID]*project.Action{}}
	tgt := func(db, schema, name string) project.Target {
		return project.Target{Database: db, Schema: schema, Name: name}
	}
	add(g, project.KindTable, "definitions/a.sqlx", tgt("p", "ds", "a"), tgt("p", "ds_x", "tmp_a"))
	add(g, project.KindView, "definitions/b.sqlx", tgt("p", "ds", "b"), tgt("p", "ds_x", "tmp_b"))
	add(g, project.KindOperation, "definitions/op.sqlx", tgt("p", "ops", "op"), tgt("p", "ops_x", "tmp_op"))
	add(g, project.KindDeclaration, "definitions/src.js", tgt("p", "raw", "orders"), tgt("p", "raw", "orders"))
	add(g, project.KindTest, "tests/t.sql", tgt("p", "ds", "t"), tgt("p", "ds_x", "t"))
	return g
}

// plainGraph is targetGraph's project compiled with no prefix or suffix.
func plainGraph() *project.Graph {
	g := &project.Graph{Actions: map[project.ActionID]*project.Action{}}
	for _, a := range targetGraph().Actions {
		b := *a
		b.Target = a.CanonicalTarget
		b.ID = b.Target.ID()
		g.Actions[b.ID] = &b
	}
	return g
}

// A reopen keeps the project on show while the new one compiles; its pages
// reload once it has, on the same action under its new name.
func TestReopenWaitsForTheNewCompile(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}}
	next := &compilingEngine{fakeEngine: newFakeEngine(targetGraph()), first: make(chan struct{}), g: targetGraph()}
	opens := 0
	s, err := New(Config{Token: "secret", Settings: f.hook(),
		Project:  &Project{Deps: frontend.Deps{Root: "/work/proj", Engine: newFakeEngine(plainGraph())}},
		Projects: func() []session.Project { return []session.Project{{Root: "/work/proj"}} },
		Open: func(session.Project) (*Project, error) {
			opens++
			return &Project{Deps: frontend.Deps{Root: "/work/proj", Engine: next}}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar
	key := s.startKey
	events := openEvents(t, c, ts.URL, key)
	for deadline := time.Now().Add(5 * time.Second); s.only().graph() == nil; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the engine's state never arrived")
		}
	}
	postJSON(t, c, ts.URL+"/api/session?project="+key, map[string]any{"selected": "p.ds.a", "recent": []string{"p.ds.a", "p.ds.b", "p.raw.orders"}}, nil)

	done := make(chan string, 1)
	go func() {
		res, err := c.Post(ts.URL+"/api/reopen?project="+key, "application/json", nil)
		if err != nil {
			done <- err.Error()
			return
		}
		_ = res.Body.Close()
		done <- res.Status
	}()
	events.until(t, "reopening", "true")
	// While it compiles, the project on show is the old one.
	var boot struct{ Project *projectBoot }
	getJSON(t, c, ts.URL+"/api/boot?project="+key, &boot)
	if boot.Project == nil || !boot.Project.State.HasGraph {
		t.Fatalf("the old project went while the new one compiled: %+v", boot.Project)
	}
	select {
	case <-done:
		t.Fatal("the reopen ended before the new compile")
	case <-time.After(50 * time.Millisecond):
	}

	close(next.first)
	// Its pages are told the new IDs, to keep their own views on them.
	events.until(t, "reload", `"p.ds.a":"p.ds_x.tmp_a"`)
	if st := <-done; st != "200 OK" {
		t.Fatalf("reopen: %s", st)
	}
	if opens != 1 {
		t.Errorf("opens = %d, want 1", opens)
	}
	getJSON(t, c, ts.URL+"/api/boot?project="+key, &boot)
	r := boot.Project.Resume
	if r.Selected != "p.ds_x.tmp_a" || !reflect.DeepEqual(r.Recent, []project.ActionID{"p.ds_x.tmp_a", "p.ds_x.tmp_b", "p.raw.orders"}) {
		t.Errorf("resume = %+v, want the actions under their new names", r)
	}
}

func TestCarrySession(t *testing.T) {
	sess := session.Session{Selected: "p.ds.a", Recent: []project.ActionID{"p.ds.a", "p.ds_x.tmp_a", "gone"}}
	got := carrySession(sess, movedActions(plainGraph(), targetGraph()))
	if got.Selected != "p.ds_x.tmp_a" || !reflect.DeepEqual(got.Recent, []project.ActionID{"p.ds_x.tmp_a", "gone"}) {
		t.Errorf("carried = %+v", got)
	}
	// With no new graph (an engine that can't say) nothing changes.
	if got := carrySession(sess, movedActions(plainGraph(), nil)); !reflect.DeepEqual(got, sess) {
		t.Errorf("carried with no graph = %+v", got)
	}
}

// Each action is found again by its file and the name a prefix doesn't
// change; a declaration keeps its ID, so it hasn't moved.
func TestMovedActions(t *testing.T) {
	got := movedActions(plainGraph(), targetGraph())
	want := map[project.ActionID]project.ActionID{
		"p.ds.a": "p.ds_x.tmp_a", "p.ds.b": "p.ds_x.tmp_b", "p.ops.op": "p.ops_x.tmp_op", "p.ds.t": "p.ds_x.t",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("moved = %v, want %v", got, want)
	}
}

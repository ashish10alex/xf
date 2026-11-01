package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/project"
)

type fakeDeferrer struct {
	ons     []bool
	forgets int
}

func (f *fakeDeferrer) Deferral(_ context.Context, req DeferralRequest, g *project.Graph, changed func(project.ActionID) bool, on bool) (*DeferralResult, error) {
	f.ons = append(f.ons, on)
	if !on {
		return &DeferralResult{Proxies: []project.Target{{Database: "d", Schema: "s", Name: "t"}}}, nil
	}
	return &DeferralResult{Deferral: &deferral.Deferral{}}, nil
}

func (f *fakeDeferrer) Forget()                             { f.forgets++ }
func (f *fakeDeferrer) Unreadable(ts []project.Target) bool { return len(ts) > 0 }

func TestSetDeferTellsClientsAndSaves(t *testing.T) {
	g := project.NewGraph([]*project.Action{{ID: "d.s.t", Target: project.Target{Database: "d", Schema: "s", Name: "t"}, Kind: project.KindTable}})
	fd := &fakeDeferrer{}
	var saved []bool
	e := New(Config{Compile: func(context.Context) (*project.Graph, error) { return g, nil }, Defer: fd, SaveDefer: func(on bool) { saved = append(saved, on) }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-e.FirstCompile()
	if e.State().Defer {
		t.Fatal("off until a client turns it on")
	}
	states, unsub := e.Subscribe()
	defer unsub()
	<-states
	e.SetDefer(true)
	select {
	case st := <-states:
		if !st.Defer {
			t.Errorf("state %+v", st)
		}
	case <-time.After(time.Second):
		t.Fatal("no state sent")
	}
	e.SetDefer(true) // no change: not saved again
	if len(saved) != 1 || !saved[0] {
		t.Errorf("saved %v", saved)
	}

	res, err := e.Deferral(ctx, DeferralRequest{Actions: []project.ActionID{"d.s.t"}})
	if err != nil || res.Deferral == nil || fd.ons[len(fd.ons)-1] != true {
		t.Errorf("on: %+v %v", res, err)
	}
	e.ForgetFailedDryRuns()
	if fd.forgets != 1 {
		t.Errorf("a run's forgetting drops the lookups: %d", fd.forgets)
	}

	e.SetDefer(false)
	res, err = e.Deferral(ctx, DeferralRequest{Actions: []project.ActionID{"d.s.t"}})
	if err != nil || res.Deferral != nil || len(res.Proxies) != 0 {
		t.Errorf("off, without proxies asked for: nothing is looked up: %+v %v", res, err)
	}
	res, _ = e.Deferral(ctx, DeferralRequest{Actions: []project.ActionID{"d.s.t"}, Proxies: true})
	if len(res.Proxies) != 1 || fd.ons[len(fd.ons)-1] != false {
		t.Errorf("off, the proxies: %+v", res)
	}
}

func TestDeferralWithoutDeferrer(t *testing.T) {
	e := New(Config{})
	if _, err := e.Deferral(context.Background(), DeferralRequest{}); !errors.Is(err, ErrNoDefer) {
		t.Errorf("err %v", err)
	}
	e.SetDefer(true)
	if e.State().Defer {
		t.Error("an engine that can't defer stays off")
	}
}

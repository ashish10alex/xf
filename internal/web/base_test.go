package web

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gitbranch"
)

// baseDeps are branchDeps comparing with develop, whose ChangedActions
// tell asked the branches they were asked about.
func baseDeps(fb *fakeBranches, asked chan<- string) frontend.Deps {
	deps := branchDeps(fb)
	now := deps.Now()
	fb.listing.Branches = append(fb.listing.Branches,
		gitbranch.Branch{Name: "develop", Date: now.Add(-3 * time.Hour), OriginDate: now.Add(-50 * time.Hour)})
	deps.DefaultBranch = "develop"
	deps.ChangedActions = func(_ context.Context, req changes.Request) (*changes.Result, error) {
		select {
		case asked <- req.DefaultBranch:
		default:
		}
		return nil, errors.New("no base")
	}
	return deps
}

func TestBaseList(t *testing.T) {
	_, ts, c := newTest(t, baseDeps(&fakeBranches{}, nil))
	var v basesView
	getJSON(t, c, ts.URL+"/api/bases", &v)
	// main is checked out, so it isn't offered; develop, the base, leads.
	if v.Base != "develop" || v.Total != 3 || len(v.Branches) != 3 {
		t.Fatalf("bases = %+v", v)
	}
	if b := v.Branches[0]; b.Name != "develop" || !b.Base || b.Tags != "base · 2d" {
		t.Errorf("first = %+v, want develop, the base, aged as origin's", b)
	}
	if b := v.Branches[1]; b.Name != "feature/orders" || b.Tags != "local only · 2h" {
		t.Errorf("second = %+v", b)
	}
	if b := v.Branches[2]; b.Name != "colleague/fix" || b.Tags != "2w" {
		t.Errorf("third = %+v", b)
	}
	getJSON(t, c, ts.URL+"/api/bases?q=colfix", &v)
	if len(v.Branches) != 1 || v.Branches[0].Name != "colleague/fix" {
		t.Errorf("q=colfix: %+v", v.Branches)
	}
}

func TestSetBase(t *testing.T) {
	asked := make(chan string, 4)
	deps := baseDeps(&fakeBranches{}, asked)
	var mu sync.Mutex
	var saved []string
	deps.SaveBranch = func(b string) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, b)
		return nil
	}
	s, ts, c := newTest(t, deps)
	drain := func() {
		for len(asked) > 0 {
			<-asked
		}
	}
	time.Sleep(50 * time.Millisecond) // the prime at open
	drain()

	var r okView
	postJSON(t, c, ts.URL+"/api/base", map[string]string{"name": "colleague/fix"}, &r)
	if !r.OK || r.Message != "comparing with colleague/fix, saved for this project" {
		t.Fatalf("set = %+v", r)
	}
	mu.Lock()
	if len(saved) != 1 || saved[0] != "colleague/fix" {
		t.Errorf("saved = %q", saved)
	}
	mu.Unlock()
	var b bootView
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &b)
	if b.Project.DefaultBranch != "colleague/fix" {
		t.Errorf("boot's base = %q", b.Project.DefaultBranch)
	}
	select {
	case got := <-asked:
		if got != "colleague/fix" {
			t.Errorf("the engine was asked about %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("the engine wasn't told the new base")
	}

	// The checked-out branch, and one there isn't, are refused.
	for _, name := range []string{"main", "nope"} {
		postJSON(t, c, ts.URL+"/api/base", map[string]string{"name": name}, &r)
		if r.OK {
			t.Errorf("%s was taken: %+v", name, r)
		}
	}
	postJSON(t, c, ts.URL+"/api/base", map[string]string{"name": "colleague/fix"}, &r)
	if !r.OK || r.Message != "already comparing with colleague/fix" {
		t.Errorf("again = %+v", r)
	}
}

func TestSetBaseUnsaved(t *testing.T) {
	_, ts, c := newTest(t, baseDeps(&fakeBranches{}, nil))
	var r okView
	postJSON(t, c, ts.URL+"/api/base", map[string]string{"name": "feature/orders"}, &r)
	if !r.OK || r.Message != "comparing with feature/orders until xf restarts: --config reads another file" {
		t.Errorf("set without a file = %+v", r)
	}
}

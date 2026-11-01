package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/session"
)

// fakeBranches is a checkout's branches, and the switches asked of it.
type fakeBranches struct {
	mu       sync.Mutex
	listing  gitbranch.Listing
	err      error
	switched []string
	refuse   error
	fetched  int
}

func (f *fakeBranches) List(context.Context) (gitbranch.Listing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listing, f.err
}

func (f *fakeBranches) Switch(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != nil {
		return f.refuse
	}
	f.switched = append(f.switched, name)
	return nil
}

func (f *fakeBranches) Fetch(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched++
	return nil
}

func (f *fakeBranches) Watch(context.Context, time.Duration) (<-chan struct{}, error) {
	return nil, errors.New("not watched")
}

func branchDeps(fb *fakeBranches) frontend.Deps {
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	fb.listing = gitbranch.Listing{Root: "/work", Branches: []gitbranch.Branch{
		{Name: "feature/orders", Date: now.Add(-2 * time.Hour)},
		{Name: "main", Current: true, Date: now.Add(-72 * time.Hour)},
		{Name: "colleague/fix", Remote: true, Date: now.Add(-20 * 24 * time.Hour)},
	}}
	return frontend.Deps{
		Engine: newFakeEngine(testGraph()), Branches: fb, Now: func() time.Time { return now },
		Git: func(context.Context) (dataformapi.GitState, error) {
			return dataformapi.GitState{Branch: "main", RemoteBranch: true, Dirty: []string{"definitions/a.sqlx"}, Behind: 2}, nil
		},
	}
}

func TestBranchIndicator(t *testing.T) {
	s, ts, c := newTest(t, branchDeps(&fakeBranches{}))
	var b bootView
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &b)
	if !b.Project.Caps.Branches {
		t.Error("the branches capability is off")
	}
	var v branchView
	getJSON(t, c, ts.URL+"/api/branch", &v)
	var text strings.Builder
	for _, p := range v.Parts {
		text.WriteString(p.Text)
	}
	if got, want := text.String(), "⎇ main ●1 ↓2"; got != want {
		t.Errorf("branch = %q, want %q", got, want)
	}
	if d := v.Parts[1].Detail; len(d) != 2 || d[1] != "definitions/a.sqlx" {
		t.Errorf("uncommitted detail = %q", d)
	}

	// Outside a repository, no parts.
	deps := branchDeps(&fakeBranches{})
	deps.Git = func(context.Context) (dataformapi.GitState, error) {
		return dataformapi.GitState{}, errors.New("not a repo")
	}
	_, ts, c = newTest(t, deps)
	getJSON(t, c, ts.URL+"/api/branch", &v)
	if len(v.Parts) != 0 {
		t.Errorf("parts outside a repository = %+v", v.Parts)
	}
}

func TestBranchList(t *testing.T) {
	_, ts, c := newTest(t, branchDeps(&fakeBranches{}))
	var v branchesView
	getJSON(t, c, ts.URL+"/api/branches", &v)
	if v.Total != 3 || len(v.Branches) != 3 || v.Branches[0].Name != "feature/orders" || v.Branches[0].Age != "2h" {
		t.Fatalf("branches = %+v", v)
	}
	if b := v.Branches[2]; !b.Remote || b.Age != "2w" {
		t.Errorf("colleague/fix = %+v", b)
	}
	if !v.Branches[1].Current {
		t.Error("main is not current")
	}
	// The project is /work/proj in the checkout at /work.
	if !strings.HasPrefix(v.Note, "switches the whole checkout at ") {
		t.Errorf("note = %q", v.Note)
	}
	getJSON(t, c, ts.URL+"/api/branches?q=colfix", &v)
	if v.Total != 3 || len(v.Branches) != 1 || v.Branches[0].Name != "colleague/fix" {
		t.Errorf("q=colfix: %+v", v)
	}

	fb := &fakeBranches{}
	deps := branchDeps(fb)
	fb.err = gitbranch.ErrNotGit
	_, ts, c = newTest(t, deps)
	getJSON(t, c, ts.URL+"/api/branches", &v)
	if !v.NotGit {
		t.Errorf("not git = %+v", v)
	}
}

func TestBranchSwitch(t *testing.T) {
	fb := &fakeBranches{}
	s, ts, c := newTest(t, branchDeps(fb))
	var r okView
	postJSON(t, c, ts.URL+"/api/branch/switch", map[string]string{"name": "feature/orders"}, &r)
	if !r.OK || r.Message != "switched to feature/orders" || len(fb.switched) != 1 {
		t.Errorf("switch = %+v, switched %v", r, fb.switched)
	}

	// git's refusal is the message.
	fb.refuse = errors.New("error: Your local changes would be overwritten\nAborting")
	postJSON(t, c, ts.URL+"/api/branch/switch", map[string]string{"name": "main"}, &r)
	if r.OK || !strings.Contains(r.Message, "Aborting") {
		t.Errorf("refused switch = %+v", r)
	}
	fb.refuse = nil

	// A local run blocks a switch; an API run doesn't.
	s.only().mu.Lock()
	s.only().run.running = true
	s.only().mu.Unlock()
	postJSON(t, c, ts.URL+"/api/branch/switch", map[string]string{"name": "main"}, &r)
	if r.OK || r.Message != frontend.BranchRunBlocker {
		t.Errorf("switch during a local run = %+v", r)
	}
	s.only().mu.Lock()
	s.only().run.api = true
	s.only().mu.Unlock()
	postJSON(t, c, ts.URL+"/api/branch/switch", map[string]string{"name": "main"}, &r)
	if !r.OK {
		t.Errorf("switch during an API run = %+v", r)
	}

	postJSON(t, c, ts.URL+"/api/branch/fetch", map[string]string{}, &r)
	if !r.OK || fb.fetched != 1 {
		t.Errorf("fetch = %+v, %d fetches", r, fb.fetched)
	}
}

func TestPickerBranches(t *testing.T) {
	s, err := New(Config{Token: "secret",
		Projects: func() []session.Project {
			return []session.Project{{Root: "/work/shop"}, {Root: "/work/shop", Backend: "dbt"}, {Root: "/work/plain"}}
		},
		Branch: func(_ context.Context, root string) (string, error) {
			if root == "/work/plain" {
				return "", gitbranch.ErrNotGit
			}
			return "feature/x", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar
	var m map[string]string
	getJSON(t, c, ts.URL+"/api/picker/branches", &m)
	if len(m) != 2 || m["/work/shop"] != "feature/x" || m["/work/plain"] != "" {
		t.Errorf("branches = %v", m)
	}
}

// The picker's sign-in banner and the settings page's rows outside a
// project come from the config's lookups, and are empty without them.
func TestPickerInfoAndAuth(t *testing.T) {
	for _, withLookups := range []bool{false, true} {
		cfg := Config{Token: "secret"}
		if withLookups {
			cfg.Landing = func(_ context.Context, emit func([]frontend.InfoRow)) { emit([]frontend.InfoRow{{Title: "binaries"}}) }
			cfg.Auth = func(context.Context) []gcpauth.Alert {
				return []gcpauth.Alert{{Message: "gcloud needs reauthentication", Command: gcpauth.CLILoginCommand}}
			}
		}
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(s.Handler())
		c := ts.Client()
		jar := &tokenJar{}
		jar.set(ts.URL)
		c.Jar = jar
		var rows []frontend.InfoRow
		var alerts []gcpauth.Alert
		getJSON(t, c, ts.URL+"/api/picker/info", &rows)
		getJSON(t, c, ts.URL+"/api/picker/auth", &alerts)
		if withLookups && (len(rows) != 1 || len(alerts) != 1 || alerts[0].Command != "gcloud auth login") {
			t.Errorf("rows %+v, alerts %+v", rows, alerts)
		}
		if !withLookups && (rows == nil || alerts == nil || len(rows)+len(alerts) != 0) {
			t.Errorf("without lookups: rows %+v, alerts %+v", rows, alerts)
		}
		ts.Close()
		s.Close()
	}
}

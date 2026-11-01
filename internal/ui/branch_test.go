package ui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/dataformapi"
	"github.com/ashish10alex/xf/internal/gitbranch"
	"github.com/ashish10alex/xf/internal/session"
)

// fakeBranches is a checkout's branches, and the switches and fetches
// asked of it.
type fakeBranches struct {
	mu       sync.Mutex
	listing  gitbranch.Listing
	err      error // of List
	switchTo []string
	switchEr error
	fetches  int
}

func (f *fakeBranches) List(context.Context) (gitbranch.Listing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listing, f.err
}

func (f *fakeBranches) Switch(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchTo = append(f.switchTo, name)
	return f.switchEr
}

func (f *fakeBranches) Fetch(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	f.listing.Branches = append(f.listing.Branches, gitbranch.Branch{Name: "fetched", Remote: true, Date: testNow()})
	return nil
}

func (f *fakeBranches) Watch(context.Context, time.Duration) (<-chan struct{}, error) {
	return nil, errors.New("not watched")
}

func newBranchModel(t *testing.T, fb *fakeBranches, g dataformapi.GitState) Model {
	t.Helper()
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m := New(Deps{
		Root:     "/work/demo",
		Engine:   newFakeEngine(fc.compile),
		StartRun: (&fakeRunner{}).start,
		Now:      testNow,
		Git:      func(context.Context) (dataformapi.GitState, error) { return g, nil },
		Branches: fb,
	})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	m = closePalette(t, m)
	return drive(t, m, nil, press("1")...) // B is "prev WORD" in the SQL pane
}

func demoBranches() *fakeBranches {
	now := testNow()
	return &fakeBranches{listing: gitbranch.Listing{Root: "/work/demo", Branches: []gitbranch.Branch{
		{Name: "feature/orders", Date: now.Add(-2 * time.Hour)},
		{Name: "main", Current: true, Date: now.Add(-3 * 24 * time.Hour)},
		{Name: "colleague/fix", Remote: true, Date: now.Add(-20 * 24 * time.Hour)},
	}}}
}

func branchesView(m Model) string { return ansi.Strip(m.branchesBox(m.geometry().bodyH)) }

func TestStatusLineBranch(t *testing.T) {
	m := newBranchModel(t, demoBranches(), dataformapi.GitState{Branch: "main", RemoteBranch: true,
		Dirty: []string{"a.sqlx", "b.sqlx"}, Ahead: 1})
	assertContains(t, screen(m), "demo · ⎇ main ●2 ↑1 · ")
	assertNotContains(t, screen(m), "↓")

	m = newBranchModel(t, demoBranches(), dataformapi.GitState{SHA: "abc1234"})
	assertContains(t, screen(m), "⎇ (detached abc1234)")

	// Outside a repository, nothing.
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	m = New(Deps{Root: "/work/demo", Engine: newFakeEngine(fc.compile), Now: testNow,
		Git: func(context.Context) (dataformapi.GitState, error) {
			return dataformapi.GitState{}, errors.New("not a repo")
		}})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	assertNotContains(t, screen(m), "⎇")
}

func TestBranchSwitcher(t *testing.T) {
	fb := demoBranches()
	m := newBranchModel(t, fb, dataformapi.GitState{Branch: "main"})
	m = drive(t, m, nil, press("B")...)
	if !m.branches.open {
		t.Fatal("B did not open the switcher")
	}
	assertContains(t, branchesView(m), "B switch branch", "3/3", "▸ feature/orders", "2h",
		"main", "current · 3d", "colleague/fix", "remote · 2w", "ctrl+r fetches")
	assertNotContains(t, branchesView(m), "whole checkout")
	assertContains(t, screen(m), "enter switch", "ctrl+r fetch origin")

	// Letters filter; enter switches to the one under the cursor.
	m = drive(t, m, nil, typeText("colfix")...)
	assertContains(t, branchesView(m), "1/3", "▸ colleague/fix")
	m = drive(t, m, nil, press("enter")...)
	if len(fb.switchTo) != 1 || fb.switchTo[0] != "colleague/fix" {
		t.Fatalf("switched to %v", fb.switchTo)
	}
	if m.branches.open {
		t.Error("the switcher stayed open after a switch")
	}
	assertContains(t, screen(m), "switched to colleague/fix")
}

func TestBranchSwitchRefused(t *testing.T) {
	fb := demoBranches()
	fb.switchEr = errors.New("error: Your local changes to the following files would be overwritten by checkout:\n\tdefinitions/a.sqlx\nAborting")
	m := newBranchModel(t, fb, dataformapi.GitState{Branch: "main"})
	m = drive(t, m, nil, press("B")...)
	m = drive(t, m, nil, press("enter")...)
	if !m.branches.open {
		t.Fatal("a refused switch closed the switcher")
	}
	assertContains(t, branchesView(m), "Your local changes", "overwritten", "definitions/a.sqlx", "Aborting")

	// enter on the current branch says so.
	m = drive(t, m, nil, press("down", "enter")...)
	assertContains(t, screen(m), "already on main")
}

func TestBranchSwitcherFetch(t *testing.T) {
	fb := demoBranches()
	m := newBranchModel(t, fb, dataformapi.GitState{Branch: "main"})
	m = drive(t, m, nil, press("B")...)
	m = drive(t, m, nil, press("ctrl+r")...)
	if fb.fetches != 1 {
		t.Fatalf("%d fetches", fb.fetches)
	}
	assertContains(t, branchesView(m), "4/4", "fetched")
}

func TestBranchSwitcherBlocks(t *testing.T) {
	fb := demoBranches()
	m := newBranchModel(t, fb, dataformapi.GitState{Branch: "main"})
	m.running, m.runAPI = true, false
	m = drive(t, m, nil, press("B")...)
	if m.branches.open {
		t.Fatal("B opened during a local run")
	}
	assertContains(t, screen(m), "a run is in progress")

	m.runAPI = true // an API run compiles the pushed branch
	m = drive(t, m, nil, press("B")...)
	if !m.branches.open {
		t.Fatal("B didn't open during an API run")
	}
}

func TestBranchSwitcherRepoRoot(t *testing.T) {
	fb := demoBranches()
	fb.listing.Root = "/work"
	m := newBranchModel(t, fb, dataformapi.GitState{Branch: "main"})
	m = drive(t, m, nil, press("B")...)
	assertContains(t, branchesView(m), "switches the whole checkout at /work")
}

func TestBranchSwitcherNotGit(t *testing.T) {
	fb := demoBranches()
	fb.err = gitbranch.ErrNotGit
	m := newBranchModel(t, fb, dataformapi.GitState{})
	m = drive(t, m, nil, press("B")...)
	if m.branches.open {
		t.Error("the switcher opened outside a repository")
	}
	assertContains(t, screen(m), "not a git repository")
}

// TestSQLPaneKeepsB: B is "prev WORD" in the SQL pane, when it has SQL.
func TestSQLPaneKeepsB(t *testing.T) {
	m := newBranchModel(t, demoBranches(), dataformapi.GitState{Branch: "main"})
	m = startOn(t, m, "demo-project.analytics.daily_orders")
	m = drive(t, m, nil, press("0", "B")...)
	if m.branches.open {
		t.Error("B opened the switcher in the SQL pane")
	}
}

func TestPickerBranches(t *testing.T) {
	projects := []session.Project{{Root: "/work/shop", Backend: "dataform"}, {Root: "/work/plain", Backend: "dbt"}}
	p := NewProjectPicker(PickerDeps{Projects: projects, Now: testNow,
		Branch: func(_ context.Context, root string) (string, error) {
			if root == "/work/plain" {
				return "", gitbranch.ErrNotGit
			}
			return "feature/x", nil
		}})
	p.width, p.height = 120, 20
	assertContains(t, ansi.Strip(p.render()), "shop   …", "plain  …")
	for _, msg := range runCmd(ReadProjectBranches(p.deps.Branch, projects)) {
		next, _ := p.Update(msg)
		p = next.(ProjectPicker)
	}
	view := ansi.Strip(p.render())
	assertContains(t, view, "shop   feature/x  dataform")
	assertNotContains(t, view, "…")
}

func TestBasePicker(t *testing.T) {
	fb := demoBranches()
	now := testNow()
	fb.listing.Branches = append(fb.listing.Branches,
		gitbranch.Branch{Name: "develop", Date: now.Add(-time.Hour), OriginDate: now.Add(-5 * time.Hour)})
	fch := &fakeChanges{}
	fc := &fakeCompiler{t: t, fixtures: []string{"basic.json"}}
	fe := newFakeEngine(fc.compile)
	fch.fe = fe
	fch.res = map[changes.Head]*changes.Result{changes.HeadWorktree: testChanges(changes.HeadWorktree)}
	var saved []string
	m := New(Deps{
		Root: "/work/demo", Engine: fe, StartRun: (&fakeRunner{}).start, Now: testNow,
		ChangedActions: fch.call, DefaultBranch: "develop", Branches: fb,
		Git:        func(context.Context) (dataformapi.GitState, error) { return dataformapi.GitState{Branch: "main"}, nil },
		SaveBranch: func(b string) error { saved = append(saved, b); return nil },
	})
	m = drive(t, m, m.Init(), tea.WindowSizeMsg{Width: 120, Height: 40})
	m = closePalette(t, m)
	assertContains(t, screen(m), "⎇ main vs develop · ")

	m = drive(t, m, nil, press("R")...)
	assertContains(t, screen(m), "b vs develop")
	m = drive(t, m, nil, press("b")...)
	if !m.branches.open || !m.branches.base || !m.changed.open {
		t.Fatal("b did not open the base picker over R")
	}
	// main is checked out, so it isn't offered; develop, the base, leads,
	// aged as origin's.
	assertContains(t, branchesView(m), "b compare with", "3/3", "▸ develop", "base · 5h",
		"feature/orders", "local only · 2h", "colleague/fix", "2w")
	assertNotContains(t, branchesView(m), "main")
	assertContains(t, screen(m), "enter compare with", "esc back")

	// A message that isn't a key, such as the cursor's blink, leaves the
	// cursor where it is.
	m = drive(t, m, nil, press("down")...)
	m = drive(t, m, nil, struct{}{})
	assertContains(t, branchesView(m), "▸ feature/orders")

	// esc goes back to R.
	m = drive(t, m, nil, press("esc")...)
	if m.branches.open || !m.changed.open {
		t.Fatal("esc did not go back to R")
	}

	m = drive(t, m, nil, press("b")...)
	m = drive(t, m, nil, typeText("colfix")...)
	m = drive(t, m, nil, press("enter")...)
	if len(saved) != 1 || saved[0] != "colleague/fix" || m.deps.DefaultBranch != "colleague/fix" {
		t.Fatalf("saved %v, base %q", saved, m.deps.DefaultBranch)
	}
	reqs := fch.requests()
	if last := reqs[len(reqs)-1]; !last.Compile || last.DefaultBranch != "colleague/fix" {
		t.Errorf("R asked again %+v", last)
	}
	if m.branches.open || !m.changed.open {
		t.Error("the pick did not go back to R")
	}
	assertContains(t, screen(m), "comparing with colleague/fix, saved for this project", "b vs colleague/fix")

	m = drive(t, m, nil, press("esc")...)
	assertContains(t, screen(m), "⎇ main vs colleague/fix")
}

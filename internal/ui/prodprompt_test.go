package ui

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/impact"
)

// fakeProdSave records the options saved, refusing the ones in bad.
type fakeProdSave struct {
	mu    sync.Mutex
	saved []string
	bad   map[string]error
}

func (f *fakeProdSave) call(_ context.Context, options string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.bad[options]; err != nil {
		return err
	}
	f.saved = append(f.saved, options)
	return nil
}

func (f *fakeProdSave) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.saved...)
}

// newProdTestModel is a model whose prodCompilerOptions nothing sets.
func newProdTestModel(t *testing.T, fi *fakeImpact, fs *fakeProdSave) Model {
	t.Helper()
	m, _ := newImpactTestModel(t, fi)
	m.deps.Unset = config.KeyProdCompilerOptions
	m.deps.SaveProdOptions = fs.call
	return m
}

func TestTheFirstImpactAsksForProdOptionsAndSavesThemOnceTheyCompile(t *testing.T) {
	fi, fs := &fakeImpact{}, &fakeProdSave{bad: map[string]error{"acme-prod": errors.New("acme-prod does not exist!")}}
	m := newProdTestModel(t, fi, fs)
	m = drive(t, m, nil, press("I")...)
	assertContains(t, screen(m), "compare with prod", "Compiler options of this project's prod runs:",
		"( ) the default targets are prod", "enter save to "+config.ProjectFile, "tab switch", "esc not now")
	if len(fi.requests()) != 0 {
		t.Fatal("checked before the prompt was answered")
	}

	// An empty box is not an answer.
	m = drive(t, m, nil, press("enter")...)
	if !m.prod.open || len(fs.all()) != 0 {
		t.Fatal("enter on an empty box saved something")
	}

	// A value the project doesn't compile with stays in the box, with why.
	m = drive(t, m, nil, append(typeText("acme-prod"), press("enter")...)...)
	assertContains(t, screen(m), "acme-prod does not exist!")
	if !m.prod.open || len(fs.all()) != 0 || len(fi.requests()) != 0 {
		t.Fatal("a value that doesn't compile was saved")
	}

	m.prod.input.SetValue("--default-database=acme-prod")
	m = drive(t, m, nil, press("enter")...)
	if got := fs.all(); len(got) != 1 || got[0] != "--default-database=acme-prod" {
		t.Fatalf("saved %q", got)
	}
	reqs := fi.requests()
	if m.prod.open || !m.impact.open || len(reqs) != 1 || reqs[0].ProdCompilerOptions != "--default-database=acme-prod" || reqs[0].Unset != "" {
		t.Fatalf("after saving: prompt open %v, I open %v, asked %+v", m.prod.open, m.impact.open, reqs)
	}

	// Answered: I doesn't ask again.
	m = drive(t, m, nil, press("esc", "I")...)
	if m.prod.open || !m.impact.open {
		t.Error("asked again")
	}
}

func TestTabPicksThatTheDefaultTargetsAreProd(t *testing.T) {
	fi, fs := &fakeImpact{}, &fakeProdSave{}
	m := newProdTestModel(t, fi, fs)
	m = drive(t, m, nil, press("I", "tab")...)
	assertContains(t, screen(m), "(•) the default targets are prod")
	m = drive(t, m, nil, press("enter")...)
	if got := fs.all(); len(got) != 1 || got[0] != "" {
		t.Fatalf("saved %q", got)
	}
	reqs := fi.requests()
	if !m.impact.open || len(reqs) != 1 || reqs[0].ProdCompilerOptions != "" || reqs[0].Unset != "" {
		t.Fatalf("asked %+v", reqs)
	}
	assertNotContains(t, screen(m), "may be dev tables")
}

func TestEscChecksAgainstTheDefaultTargetsAndTheReportSaysSo(t *testing.T) {
	fi, fs := &fakeImpact{}, &fakeProdSave{}
	fi.report = testReport()
	fi.report.Against = []string{"acme-dev"}
	fi.report.Unset = config.KeyProdCompilerOptions // as the engine answers a request that says so
	fi.report.New = []impact.Table{
		{Table: "acme-dev.staging.fresh", Type: "table", New: true},
		{Table: "acme-dev.staging.back", Type: "table", New: true, Exists: true},
	}
	fi.report.AtRisk[0].New = true
	m := newProdTestModel(t, fi, fs)
	m = drive(t, m, nil, press("I", "esc")...)
	reqs := fi.requests()
	if len(fs.all()) != 0 || len(reqs) != 1 || reqs[0].Unset != config.KeyProdCompilerOptions {
		t.Fatalf("saved %q, asked %+v", fs.all(), reqs)
	}
	assertContains(t, screen(m), "compared with the default targets in acme-dev",
		"⚠ Compared with the project's default targets (acme-dev), which may be dev tables. Set",
		"1 to fix before merging.",
		"analytics.daily_orders  new, its table is already there; drops order_total",
		"staging.fresh", "new: nothing reads it yet",
		"staging.back", "new, its table is already there; keeps every column")

	// Not asked again while this xf runs, for C either.
	m = drive(t, m, nil, press("esc", "I")...)
	if m.prod.open {
		t.Error("asked again after esc")
	}
}

func TestCostAsksTooButNotWhereTheOptionsAreSetOrInDbt(t *testing.T) {
	fc, fs := &fakeCost{}, &fakeProdSave{}
	m, _ := newCostTestModel(t, fc)
	m.deps.Unset = config.KeyProdCompilerOptions
	m.deps.SaveProdOptions = fs.call
	m = drive(t, m, nil, press("C")...)
	if !m.prod.open || m.cost.open {
		t.Fatal("C didn't ask")
	}
	m = drive(t, m, nil, press("esc")...)
	if reqs := fc.requests(); !m.cost.open || len(reqs) != 1 || reqs[0].Unset != config.KeyProdCompilerOptions {
		t.Fatalf("after esc: C open %v, asked %+v", m.cost.open, reqs)
	}

	// Set, even to "": nothing to ask.
	m, _ = newCostTestModel(t, &fakeCost{})
	m.deps.SaveProdOptions = fs.call
	if m = drive(t, m, nil, press("C")...); m.prod.open {
		t.Error("asked though the options are set")
	}

	// dbt has no prodCompilerOptions: its report says the target wasn't
	// chosen, and nothing is asked.
	fc = &fakeCost{}
	m, _ = newCostTestModel(t, fc)
	m.deps.Unset = config.KeyDbtTarget
	m = drive(t, m, nil, press("C")...)
	if reqs := fc.requests(); m.prod.open || len(reqs) != 1 || reqs[0].Unset != config.KeyDbtTarget {
		t.Fatalf("dbt: prompt open %v, asked %+v", m.prod.open, reqs)
	}
}

func TestImpactMarksAChangeTheBaseBranchAlreadyMade(t *testing.T) {
	fi := &fakeImpact{report: testReport()}
	fi.report.AtRisk[0].Columns[0].Change.OnBase = true
	m, _ := newImpactTestModel(t, fi)
	m = drive(t, m, nil, press("I")...)
	assertContains(t, screen(m), "Nothing to fix.",
		"already changed on origin/main:", "low",
		`"Already changed on origin/main": origin/main makes the change too`)
	assertNotContains(t, screen(m), "FIX")
}

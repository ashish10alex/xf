package frontend

import (
	"slices"
	"testing"

	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/project"
)

func tgt(db, schema, name string) project.Target {
	return project.Target{Database: db, Schema: schema, Name: name}
}

func TestCheckDeferredRun(t *testing.T) {
	prodA, prodB := tgt("p", "s", "a"), tgt("p", "s", "b")
	fresh := deferral.Entry{Dev: tgt("d", "s", "a"), Prod: &prodA, Status: deferral.Deferred}
	stale := deferral.Entry{Dev: tgt("d", "s", "b"), Prod: &prodB, Status: deferral.Deferred, Stale: true}
	routine := deferral.Entry{Dev: tgt("d", "s", "f"), Prod: &prodA, Status: deferral.Deferred, Routine: true}
	missing := deferral.Entry{Dev: tgt("d", "s", "m"), Status: deferral.MissingEverywhere}

	if c := CheckDeferredRun(DeferRun{Proxies: []project.Target{tgt("d", "s", "a")}}); c.Stop != DeferStopLeftover || len(c.Detail) != 1 {
		t.Errorf("off, leftover views: %+v", c)
	}
	if c := CheckDeferredRun(DeferRun{Proxies: []project.Target{tgt("d", "s", "a")}, Answers: map[string]bool{DeferAnswerLeftover: true}}); c.Stop != DeferGo {
		t.Errorf("off, leftovers allowed: %+v", c)
	}
	on := func(es ...deferral.Entry) DeferRun {
		return DeferRun{On: true, Deferral: &deferral.Deferral{Entries: es}, Answers: map[string]bool{}}
	}
	if c := CheckDeferredRun(on(fresh, routine)); !c.AddRoutines || !slices.Equal(c.Add, []project.ActionID{"d.s.f"}) {
		t.Errorf("a routine is built: %+v", c)
	}
	if c := CheckDeferredRun(on(fresh, missing)); c.Stop != DeferStopBlocked {
		t.Errorf("one in neither place: %+v", c)
	}
	r := on(fresh)
	r.MissingDatasets = func([]deferral.Entry) map[string]bool { return map[string]bool{"d.s": true} }
	if c := CheckDeferredRun(r); c.Stop != DeferStopBlocked {
		t.Errorf("no dev dataset: %+v", c)
	}
	if c := CheckDeferredRun(on(fresh, stale)); c.Stop != DeferStopStale || len(c.Detail) != 1 {
		t.Errorf("stale: %+v", c)
	}
	r = on(fresh, stale)
	r.Answers[DeferAnswerChanged] = true
	if c := CheckDeferredRun(r); !slices.Equal(c.Add, []project.ActionID{"d.s.b"}) || c.AddRoutines {
		t.Errorf("build changed first: %+v", c)
	}
	r = on(fresh, stale)
	r.Answers[DeferAnswerStale] = true
	if c := CheckDeferredRun(r); c.Stop != DeferGo || len(c.Entries) != 2 {
		t.Errorf("stale allowed: %+v", c)
	}
	r = on(fresh)
	r.API = true
	if c := CheckDeferredRun(r); c.Stop != DeferStopAPI {
		t.Errorf("an API run: %+v", c)
	}
	r.APIApproved = true
	if c := CheckDeferredRun(r); c.Stop != DeferGo || len(c.Entries) != 1 {
		t.Errorf("an approved API run: %+v", c)
	}
}

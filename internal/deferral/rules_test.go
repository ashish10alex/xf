package deferral

import (
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

func tgt(db, schema, name string) project.Target {
	return project.Target{Database: db, Schema: schema, Name: name}
}

// act is an action at t whose canonical target is canon.
func act(t, canon project.Target, kind project.ActionKind, deps ...project.Target) *project.Action {
	return &project.Action{ID: t.ID(), Target: t, CanonicalTarget: canon, Kind: kind, DependencyTargets: deps}
}

// A dev compile with a schema suffix and a prod compile in another
// project: orders reads customers (not built in dev), payments (built in
// dev), a declaration and a no-output operation.
func fixture() (dev, prod *project.Graph) {
	canon := func(name string) project.Target { return tgt("dev-proj", "sales", name) }
	dt := func(name string) project.Target { return tgt("dev-proj", "sales_dev", name) }
	decl := tgt("raw-proj", "raw", "events")
	op := dt("cleanup")
	dev = project.NewGraph([]*project.Action{
		act(dt("orders"), canon("orders"), project.KindTable, dt("customers"), dt("payments"), decl, op),
		act(dt("customers"), canon("customers"), project.KindTable),
		act(dt("payments"), canon("payments"), project.KindView),
		act(decl, decl, project.KindDeclaration),
		act(op, canon("cleanup"), project.KindOperation),
	})
	pc := func(name string) project.Target { return tgt("prod-proj", "sales", name) }
	prod = project.NewGraph([]*project.Action{
		act(pc("orders"), pc("orders"), project.KindTable),
		act(pc("customers"), pc("customers"), project.KindTable),
		act(pc("payments"), pc("payments"), project.KindView),
		act(decl, decl, project.KindDeclaration),
		act(pc("cleanup"), pc("cleanup"), project.KindOperation),
	})
	return dev, prod
}

func TestProdTargetsMatchByCanonicalTarget(t *testing.T) {
	dev, prod := fixture()
	p := NewProdTargets(prod)
	got, ok := p.Of(dev.Actions["dev-proj.sales_dev.customers"])
	if !ok || got != tgt("prod-proj", "sales", "customers") {
		t.Fatalf("prod of customers = %v %v", got, ok)
	}
	if SameAsDev(dev, p) {
		t.Error("prod is another project: not the same as dev")
	}
	if !SameAsDev(prod, p) {
		t.Error("prod compared with itself is the same")
	}
}

func TestProdTargetsSkipAmbiguousShortKeys(t *testing.T) {
	a := tgt("p1", "s", "t")
	b := tgt("p2", "s", "t")
	p := NewProdTargets(project.NewGraph([]*project.Action{act(a, a, project.KindTable), act(b, b, project.KindTable)}))
	if _, ok := p.Of(act(tgt("p3", "s_dev", "t"), tgt("p3", "s", "t"), project.KindTable)); ok {
		t.Error("two prod actions share s.t: an action in a third database matches neither")
	}
	if got, ok := p.Of(act(tgt("p1", "s_dev", "t"), a, project.KindTable)); !ok || got != a {
		t.Errorf("same database matches by whole canonical target: %v %v", got, ok)
	}
}

func TestCandidatesAndDecide(t *testing.T) {
	dev, prod := fixture()
	p := NewProdTargets(prod)
	cands := Candidates(dev, []project.ActionID{"dev-proj.sales_dev.orders"}, p)
	var ids []string
	for _, c := range cands {
		ids = append(ids, string(c.Dev.ID()))
	}
	if !slices.Equal(ids, []string{"dev-proj.sales_dev.customers", "dev-proj.sales_dev.payments"}) {
		t.Fatalf("candidates %v: a declaration and an operation without output are left out", ids)
	}
	// Selecting customers too keeps it out.
	if c := Candidates(dev, []project.ActionID{"dev-proj.sales_dev.orders", "dev-proj.sales_dev.customers"}, p); len(c) != 1 {
		t.Errorf("a Selected Action is never a candidate: %v", c)
	}
	entries := Decide(cands,
		func(t project.Target) bool { return t.Name == "payments" },
		func(project.Target) ProdStatus { return ProdExists })
	if len(entries) != 1 || entries[0].Dev.Name != "customers" || entries[0].Status != Deferred || !entries[0].Read() {
		t.Fatalf("entries %+v", entries)
	}
	unreadable := Decide(cands, func(project.Target) bool { return false }, func(project.Target) ProdStatus { return ProdUnreadable })
	missing := Decide(cands, func(project.Target) bool { return false }, func(project.Target) ProdStatus { return ProdMissing })
	if unreadable[0].Status != Unreadable || missing[0].Status != MissingEverywhere || unreadable[0].Read() {
		t.Errorf("unreadable %+v missing %+v", unreadable, missing)
	}
}

func TestRewrite(t *testing.T) {
	dev, prod := tgt("d", "s_dev", "customers"), tgt("p", "s", "customers")
	entries := []Entry{{Dev: dev, Prod: &prod, Status: Deferred}, {Dev: tgt("d", "s_dev", "x"), Status: MissingEverywhere}}
	sql := "select *\nfrom `d.s_dev.customers` c\njoin `d.s_dev.customers_v2` v\njoin `d.s_dev.x`"
	got := Rewrite(sql, entries)
	want := "select *\nfrom `p.s.customers` c\njoin `d.s_dev.customers_v2` v\njoin `d.s_dev.x`"
	if got != want {
		t.Errorf("rewrite:\n%s\nwant:\n%s", got, want)
	}
	if Rewrite(got, entries) != got {
		t.Error("rewriting twice changes nothing more")
	}
	a := &project.Action{Query: sql, PreOps: []string{"declare x default (select 1 from `d.s_dev.customers`)"}}
	r := RewriteAction(a, entries)
	if r == a || r.Query != want || !strings.Contains(r.PreOps[0], "`p.s.customers`") || a.Query != sql {
		t.Errorf("RewriteAction %+v; the action given is left alone", r)
	}
	if RewriteAction(a, entries[1:]) != a {
		t.Error("nothing deferred: the action itself")
	}
}

func TestAccessDenied(t *testing.T) {
	got := AccessDenied("Access Denied: Table prod-proj:sales.customers: User does not have permission. Access Denied: Table p:s.t.")
	want := []project.Target{tgt("prod-proj", "sales", "customers"), tgt("p", "s", "t")}
	if !slices.Equal(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestRunSet(t *testing.T) {
	dev, _ := fixture()
	got := RunSet(dev, project.RunOptions{Actions: []project.ActionID{"dev-proj.sales_dev.orders"}})
	if !slices.Equal(got, []project.ActionID{"dev-proj.sales_dev.orders"}) {
		t.Errorf("alone: %v", got)
	}
	got = RunSet(dev, project.RunOptions{Actions: []project.ActionID{"dev-proj.sales_dev.orders"}, IncludeDeps: true})
	if slices.Contains(got, "raw-proj.raw.events") || !slices.Contains(got, "dev-proj.sales_dev.customers") || len(got) != 4 {
		t.Errorf("with dependencies, never a declaration: %v", got)
	}
	got = RunSet(dev, project.RunOptions{Actions: []project.ActionID{"dev-proj.sales_dev.customers"}, IncludeDependents: true})
	if !slices.Equal(got, []project.ActionID{"dev-proj.sales_dev.customers", "dev-proj.sales_dev.orders"}) {
		t.Errorf("with dependents: %v", got)
	}
}

func TestRoutines(t *testing.T) {
	fn := &project.Action{Kind: project.KindOperation, HasOutput: true, Queries: []string{"CREATE OR REPLACE TEMP FUNCTION f(x INT64) AS (x)"}}
	if !IsRoutine(fn) {
		t.Error("an operation that makes a function is a routine")
	}
	if IsRoutine(&project.Action{Kind: project.KindOperation, HasOutput: true, Queries: []string{"create table t as select 1"}}) {
		t.Error("an operation that makes a table is not")
	}
}

func TestStepFor(t *testing.T) {
	prod := tgt("p", "s", "t")
	proxy := map[string]string{ProxyLabel: "true"}
	cases := []struct {
		labels map[string]string
		query  string
		exists bool
		want   ProxyStep
	}{
		{nil, "", false, ProxyCreate},
		{proxy, ProxyQuery(prod), true, ProxyKeep},
		{proxy, "SELECT * FROM `p.s.other`", true, ProxyUpdate},
		{nil, ProxyQuery(prod), true, ProxyLeave},
	}
	for _, c := range cases {
		if got := StepFor(c.labels, c.query, c.exists, prod); got != c.want {
			t.Errorf("%+v: got %v want %v", c, got, c.want)
		}
	}
}

func TestFor(t *testing.T) {
	dev, _ := fixture()
	p := tgt("p", "s", "customers")
	d := &Deferral{Entries: []Entry{{Dev: tgt("dev-proj", "sales_dev", "customers"), Prod: &p, Status: Deferred}, {Dev: tgt("x", "y", "z"), Status: MissingEverywhere}}}
	got := d.For(dev.Actions["dev-proj.sales_dev.orders"])
	if len(got.Entries) != 1 || got.Deferred() != 1 {
		t.Errorf("For keeps the action's own upstream: %+v", got)
	}
}

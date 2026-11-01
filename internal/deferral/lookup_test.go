package deferral

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/project"
)

// fakeBQ is BigQuery holding tables by "db.schema" and the views made.
type fakeBQ struct {
	mu       sync.Mutex
	datasets map[string][]bigquery.LabelledTable
	prod     map[string]error // "db.schema.name" → nil (exists) or an error
	views    map[string]*bigquery.ViewInfo
	lists    int
	created  []bigquery.ViewSpec
	updated  []bigquery.ViewSpec
	deleted  []string
}

func (f *fakeBQ) ListTablesLabelled(_ context.Context, db, schema string) ([]bigquery.LabelledTable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	ts, ok := f.datasets[db+"."+schema]
	if !ok {
		return nil, &bigquery.APIError{Status: http.StatusNotFound, Message: "not found"}
	}
	return ts, nil
}

func (f *fakeBQ) ListRoutines(context.Context, string, string) ([]string, error) { return nil, nil }

func (f *fakeBQ) Exists(_ context.Context, db, schema, name string, _ bool) (bool, error) {
	err, ok := f.prod[db+"."+schema+"."+name]
	if !ok {
		return false, nil
	}
	return err == nil, err
}

func (f *fakeBQ) View(_ context.Context, db, schema, name string) (*bigquery.ViewInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.views[db+"."+schema+"."+name], nil
}

func (f *fakeBQ) CreateView(_ context.Context, v bigquery.ViewSpec) error {
	f.created = append(f.created, v)
	return nil
}

func (f *fakeBQ) UpdateView(_ context.Context, v bigquery.ViewSpec) error {
	f.updated = append(f.updated, v)
	return nil
}

func (f *fakeBQ) DeleteTable(_ context.Context, db, schema, name string) error {
	f.deleted = append(f.deleted, db+"."+schema+"."+name)
	return nil
}

func lookupsOf(f *fakeBQ) *Lookups {
	return NewLookups(func(context.Context) (BigQuery, error) { return f, nil }, nil)
}

func TestResolve(t *testing.T) {
	dev, prod := fixture()
	f := &fakeBQ{
		datasets: map[string][]bigquery.LabelledTable{"dev-proj.sales_dev": {{ID: "payments", Type: "VIEW"}}},
		prod:     map[string]error{"prod-proj.sales.customers": nil},
		views:    map[string]*bigquery.ViewInfo{"dev-proj.sales_dev.payments": {LastModified: time.UnixMilli(1000)}},
	}
	l := lookupsOf(f)
	changed := func(id project.ActionID) bool { return id == "dev-proj.sales_dev.customers" }
	d, err := Resolve(context.Background(), dev, []project.ActionID{"dev-proj.sales_dev.orders"}, NewProdTargets(prod), l, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) != 1 || !d.Entries[0].Read() || !d.Entries[0].Stale || d.Entries[0].Prod.Name != "customers" {
		t.Fatalf("entries %+v", d.Entries)
	}
	if len(d.BuiltInDev) != 1 || d.BuiltInDev[0].Dev.Name != "payments" || d.BuiltInDev[0].LastModified != 1000 {
		t.Fatalf("built in dev %+v", d.BuiltInDev)
	}
	if _, err := Resolve(context.Background(), dev, []project.ActionID{"dev-proj.sales_dev.orders"}, NewProdTargets(prod), l, nil); err != nil || f.lists != 1 {
		t.Errorf("a second Resolve lists nothing again: %d lists, %v", f.lists, err)
	}
	if l.MarkUnreadable([]project.Target{tgt("prod-proj", "sales", "customers")}) {
		d, _ = Resolve(context.Background(), dev, []project.ActionID{"dev-proj.sales_dev.orders"}, NewProdTargets(prod), l, nil)
		if d.Entries[0].Status != Unreadable {
			t.Errorf("a denied prod table is unreadable: %+v", d.Entries)
		}
	} else {
		t.Error("MarkUnreadable says it is new")
	}
}

func TestResolveProxyCountsAsMissingAndForbiddenProd(t *testing.T) {
	dev, prod := fixture()
	f := &fakeBQ{
		datasets: map[string][]bigquery.LabelledTable{"dev-proj.sales_dev": {{ID: "customers", Labels: map[string]string{ProxyLabel: "true"}}}},
		prod:     map[string]error{"prod-proj.sales.payments": &bigquery.APIError{Status: http.StatusForbidden}, "prod-proj.sales.customers": nil},
	}
	l := lookupsOf(f)
	d, err := Resolve(context.Background(), dev, []project.ActionID{"dev-proj.sales_dev.orders"}, NewProdTargets(prod), l, nil)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]Status{}
	for _, e := range d.Entries {
		statuses[e.Dev.Name] = e.Status
	}
	if statuses["customers"] != Deferred || statuses["payments"] != Unreadable {
		t.Errorf("statuses %v", statuses)
	}
	proxies, err := Proxies(context.Background(), dev, []project.ActionID{"dev-proj.sales_dev.orders"}, l)
	if err != nil || len(proxies) != 1 || proxies[0].Name != "customers" {
		t.Errorf("proxies %v %v", proxies, err)
	}
}

func TestBuiltInDev(t *testing.T) {
	f := &fakeBQ{
		datasets: map[string][]bigquery.LabelledTable{"dev-proj.sales_dev": {
			{ID: "agg"}, {ID: "proxied", Labels: map[string]string{ProxyLabel: "true"}},
		}},
		views: map[string]*bigquery.ViewInfo{"dev-proj.sales_dev.agg": {LastModified: time.UnixMilli(2000)}},
	}
	l := lookupsOf(f)
	ctx := context.Background()
	if ok, ms, err := l.BuiltInDev(ctx, tgt("dev-proj", "sales_dev", "agg")); !ok || ms != 2000 || err != nil {
		t.Errorf("a built table: %v %d %v", ok, ms, err)
	}
	for _, name := range []string{"proxied", "never_built"} {
		if ok, _, err := l.BuiltInDev(ctx, tgt("dev-proj", "sales_dev", name)); ok || err != nil {
			t.Errorf("%s isn't built in dev: %v %v", name, ok, err)
		}
	}
	if ok, _, err := l.BuiltInDev(ctx, tgt("dev-proj", "no_dataset", "agg")); ok || err != nil {
		t.Errorf("a dataset not made yet has nothing built: %v %v", ok, err)
	}
}

func TestResolveSameAsDev(t *testing.T) {
	dev, _ := fixture()
	_, err := Resolve(context.Background(), dev, []project.ActionID{"dev-proj.sales_dev.orders"}, NewProdTargets(dev), lookupsOf(&fakeBQ{}), nil)
	if !errors.Is(err, ErrSameAsDev) {
		t.Errorf("err %v", err)
	}
}

func TestResolveListingFails(t *testing.T) {
	dev, prod := fixture()
	l := NewLookups(func(context.Context) (BigQuery, error) { return nil, errors.New("no credentials") }, nil)
	if _, err := Resolve(context.Background(), dev, []project.ActionID{"dev-proj.sales_dev.orders"}, NewProdTargets(prod), l, nil); err == nil {
		t.Error("a dataset that can't be listed fails the Deferral rather than guess")
	}
}

func TestEnsureProxies(t *testing.T) {
	p1, p2, p3 := tgt("p", "s", "a"), tgt("p", "s", "b"), tgt("p", "s", "c")
	f := &fakeBQ{views: map[string]*bigquery.ViewInfo{
		"d.s.b": {Labels: map[string]string{ProxyLabel: "true"}, Query: ProxyQuery(p2)},
		"d.s.c": {Labels: map[string]string{ProxyLabel: "true"}, Query: "SELECT * FROM `p.s.old`"},
	}}
	entries := []Entry{
		{Dev: tgt("d", "s", "a"), Prod: &p1, Status: Deferred},
		{Dev: tgt("d", "s", "b"), Prod: &p2, Status: Deferred},
		{Dev: tgt("d", "s", "c"), Prod: &p3, Status: Deferred},
		{Dev: tgt("d", "s", "fn"), Prod: &p3, Status: Deferred, Routine: true},
		{Dev: tgt("d", "s", "gone"), Status: MissingEverywhere},
	}
	r, err := EnsureProxies(context.Background(), f, entries)
	if err != nil {
		t.Fatal(err)
	}
	if r != (ProxyResult{Created: 1, Updated: 1, Kept: 1}) || r.String() != "made 1, repointed 1 proxy views" {
		t.Errorf("result %+v %q", r, r.String())
	}
	if f.created[0].Labels[ProxyLabel] != "true" || f.created[0].Query != "SELECT * FROM `p.s.a`" || f.created[0].Table != "a" {
		t.Errorf("created %+v", f.created)
	}
}

func TestRemoveProxies(t *testing.T) {
	dev, _ := fixture()
	f := &fakeBQ{datasets: map[string][]bigquery.LabelledTable{"dev-proj.sales_dev": {
		{ID: "customers", Labels: map[string]string{ProxyLabel: "true"}}, {ID: "payments"},
	}}}
	found, err := RemoveProxies(context.Background(), f, dev, false)
	if err != nil || len(found) != 1 || len(f.deleted) != 0 {
		t.Errorf("finding removes nothing: %v %v", found, f.deleted)
	}
	removed, err := RemoveProxies(context.Background(), f, dev, true)
	if err != nil || len(removed) != 1 || len(f.deleted) != 1 || f.deleted[0] != "dev-proj.sales_dev.customers" {
		t.Errorf("removed %v deleted %v err %v", removed, f.deleted, err)
	}
}

package dbt

import (
	"slices"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

func TestDeferred(t *testing.T) {
	tg := func(db, schema, name string) project.Target {
		return project.Target{Database: db, Schema: schema, Name: name}
	}
	model := func(t project.Target, name string, kind project.ActionKind, query string, deps ...project.Target) *project.Action {
		return &project.Action{ID: t.ID(), Target: t, Kind: kind, RunName: "shop." + name, Query: query, DependencyTargets: deps}
	}
	devCust, devPay, devEph := tg("dev", "dbt_me", "customers"), tg("dev", "dbt_me", "payments"), tg("dev", "dbt_me", "eph")
	devNew, devOrders := tg("dev", "dbt_me", "brand_new"), tg("dev", "dbt_me", "orders")
	dev := project.NewGraph([]*project.Action{
		model(devCust, "customers", project.KindTable, "select 1"),
		model(devPay, "payments", project.KindTable, "select 1"),
		model(devEph, "eph", project.KindEphemeral, "", devPay),
		model(devNew, "brand_new", project.KindTable, "select 1"),
		model(devOrders, "orders", project.KindTable,
			"select * from `prod`.`shop`.`customers` join `dev`.`dbt_me`.`payments` join `dev`.`dbt_me`.`brand_new`",
			devCust, devEph, devNew),
	})
	prod := project.NewGraph([]*project.Action{
		model(tg("prod", "shop", "customers"), "customers", project.KindTable, ""),
		model(tg("prod", "shop", "payments"), "payments", project.KindTable, ""),
		model(tg("prod", "shop", "orders"), "orders", project.KindTable, ""),
	})
	got := Deferred(dev, prod, []project.ActionID{devOrders.ID()})
	if len(got) != 2 {
		t.Fatalf("got %+v: a model prod hasn't is left out", got)
	}
	if got[0].Dev != devCust || got[0].Prod == nil || *got[0].Prod != tg("prod", "shop", "customers") {
		t.Errorf("customers is read from prod: %+v", got[0])
	}
	if got[1].Dev != devPay || got[1].Prod != nil {
		t.Errorf("payments, through the ephemeral model, is read from dev: %+v", got[1])
	}
	if !slices.Equal(DeferArgs("/s"), []string{"--defer", "--defer-state", "/s"}) || DeferArgs("") != nil {
		t.Error("DeferArgs")
	}
	b := New("dbt", "dev", nil, false)
	if args := b.RunArgs(project.RunOptions{Names: []string{"shop.orders"}, DeferState: "/s"}); !slices.Contains(args, "--defer-state") {
		t.Errorf("a deferred run: %v", args)
	}
}

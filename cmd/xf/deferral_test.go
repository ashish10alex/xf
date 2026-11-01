package main

import (
	"context"
	"io"
	"log"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

func TestProdCompileIsKeptUntilAnActionIsMissing(t *testing.T) {
	tg := func(db, name string) project.Target { return project.Target{Database: db, Schema: "s", Name: name} }
	act := func(t, canon project.Target, deps ...project.Target) *project.Action {
		return &project.Action{ID: t.ID(), Target: t, CanonicalTarget: canon, Kind: project.KindTable, DependencyTargets: deps}
	}
	dev := project.NewGraph([]*project.Action{
		act(tg("dev", "a"), tg("dev", "a")),
		act(tg("dev", "new"), tg("dev", "new")),
		act(tg("dev", "b"), tg("dev", "b"), tg("dev", "a")),
		act(tg("dev", "c"), tg("dev", "c"), tg("dev", "new")),
	})
	compiles := 0
	d := &dataformDeferrer{root: t.TempDir(), logger: log.New(io.Discard, "", 0), prod: map[string]*prodCompile{},
		compile: func(ctx context.Context, args []string) (*project.Graph, error) {
			compiles++
			if len(args) != 1 || args[0] != "--default-database=prod" {
				t.Errorf("args %v", args)
			}
			return project.NewGraph([]*project.Action{act(tg("prod", "a"), tg("prod", "a")), act(tg("prod", "b"), tg("prod", "b"))}), nil
		}}
	ctx := context.Background()
	opts := "--default-database=prod"
	p, err := d.prodTargets(ctx, opts, dev, []project.ActionID{"dev.s.b"})
	if err != nil || compiles != 1 {
		t.Fatalf("first: %d compiles, %v", compiles, err)
	}
	if got, ok := p.Of(dev.Actions["dev.s.a"]); !ok || got != tg("prod", "a") {
		t.Errorf("prod of a: %v", got)
	}
	_, _ = d.prodTargets(ctx, opts, dev, []project.ActionID{"dev.s.b"})
	if compiles != 1 {
		t.Errorf("kept: %d compiles", compiles)
	}
	// c reads new, which prod hasn't: compiled again once, then known missing.
	_, _ = d.prodTargets(ctx, opts, dev, []project.ActionID{"dev.s.c"})
	_, _ = d.prodTargets(ctx, opts, dev, []project.ActionID{"dev.s.c"})
	if compiles != 2 {
		t.Errorf("a missing action compiles again once: %d compiles", compiles)
	}
}

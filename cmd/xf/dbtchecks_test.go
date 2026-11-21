package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/project"
)

func TestUnsetDbtProdTargetOnlyWhenNothingPicksATarget(t *testing.T) {
	set := func(keys ...string) config.Loaded {
		l := config.Loaded{Sources: map[string]string{}}
		for _, k := range keys {
			l.Sources[k] = "project"
		}
		return l
	}
	none := func(string) string { return "" }
	env := func(k string) string {
		if k == "DBT_TARGET" {
			return "ci"
		}
		return ""
	}
	cases := []struct {
		name     string
		settings config.Loaded
		getenv   func(string) string
		want     string
	}{
		{"nothing", set(), none, config.KeyDbtProdTarget},
		{"dbtProdTarget", set(config.KeyDbtProdTarget), none, ""},
		{"dbtTarget", set(config.KeyDbtTarget), none, ""},
		{"$DBT_TARGET", set(), env, ""},
	}
	for _, c := range cases {
		if got := unsetDbtProdTarget(c.settings, c.getenv); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDbtTargetsCompileOtherTargetsApart(t *testing.T) {
	targets := newDbtTargets(dbtBackendForTest("dev"), t.TempDir(), nil)
	if targets.other("") || targets.other("dev") || !targets.other("prod") {
		t.Error("only another target than xf's needs a compile of its own")
	}
	if targets.backend("dev") != targets.b || targets.backend("") != targets.b {
		t.Error("xf's own target should use xf's backend")
	}
	prod := targets.backend("prod")
	if prod.Target != "prod" || prod.Bin != targets.b.Bin || prod.Variant() == targets.b.Variant() || targets.backend("prod") != prod {
		t.Errorf("prod backend %+v", prod)
	}
}

func dbtBackendForTest(target string) *dbt.Backend {
	return dbt.New("/usr/bin/true", target, func(root string) string { return root }, false)
}

func TestWorktreeCompilesCoalesceAndInvalidateBySignature(t *testing.T) {
	// 1. dbtTargets.worktree coalesces concurrent calls for the same signature,
	// caches the result, invalidates on signature change, and never caches errors.
	targets := newDbtTargets(dbtBackendForTest("dev"), t.TempDir(), nil)
	var dbtCalls atomic.Int32
	gate := make(chan struct{})
	failNext := atomic.Bool{}
	targets.compileWT = func(ctx context.Context, target string) (*project.Graph, error) {
		n := dbtCalls.Add(1)
		if n == 1 {
			<-gate
		}
		if failNext.Load() {
			return nil, errors.New("boom")
		}
		return project.NewGraph(nil), nil
	}

	var wg sync.WaitGroup
	graphs := make([]*project.Graph, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			graphs[i], errs[i] = targets.worktree(context.Background(), "prod", "sig-1")
		})
	}
	close(gate)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || graphs[0] == nil || graphs[0] != graphs[1] {
		t.Fatalf("coalesced dbt worktree: graphs=%v errs=%v", graphs, errs)
	}
	if dbtCalls.Load() != 1 {
		t.Fatalf("dbtCalls = %d, want 1", dbtCalls.Load())
	}
	// New signature triggers a fresh compile; failure isn't cached.
	failNext.Store(true)
	if _, err := targets.worktree(context.Background(), "prod", "sig-2"); err == nil {
		t.Fatal("expected error for sig-2")
	}
	failNext.Store(false)
	if _, err := targets.worktree(context.Background(), "prod", "sig-2"); err != nil {
		t.Fatalf("retry after failure failed: %v", err)
	}
	if dbtCalls.Load() != 3 {
		t.Fatalf("dbtCalls = %d, want 3", dbtCalls.Load())
	}

	// 2. optionSources.worktree coalesces concurrent Dataform prod compiles.
	opts := newOptionSources("dataform", t.TempDir(), nil)
	var dfCalls atomic.Int32
	dfGate := make(chan struct{})
	opts.compileWT = func(ctx context.Context, args []string) (*project.Graph, error) {
		if dfCalls.Add(1) == 1 {
			<-dfGate
		}
		return project.NewGraph(nil), nil
	}
	for i := range 2 {
		wg.Go(func() {
			graphs[i], errs[i] = opts.worktree(context.Background(), "--database-suffix=prod", []string{"--database-suffix=prod"}, "sig-a")
		})
	}
	close(dfGate)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || graphs[0] == nil || graphs[0] != graphs[1] || dfCalls.Load() != 1 {
		t.Fatalf("coalesced dataform worktree: calls=%d graphs=%v errs=%v", dfCalls.Load(), graphs, errs)
	}
	// Expired TTL forces a fresh compile (so external profiles.yml edits aren't stale).
	opts.mu.Lock()
	opts.wt["--database-suffix=prod"].finishedAt = time.Now().Add(-2 * worktreeShareTTL)
	opts.mu.Unlock()
	if _, err := opts.worktree(context.Background(), "--database-suffix=prod", []string{"--database-suffix=prod"}, "sig-a"); err != nil {
		t.Fatal(err)
	}
	if dfCalls.Load() != 2 {
		t.Fatalf("after TTL expiry: dfCalls = %d, want 2", dfCalls.Load())
	}
}

func TestProgressPrinterDeduplicatesPerCheck(t *testing.T) {
	var buf bytes.Buffer
	p := &progressPrinter{w: &buf, phases: map[string]string{}}
	p.say("Column impact: Dry running 1/4")
	p.say("Query cost: Dry running 1/4")
	p.say("Column impact: Dry running 2/4")
	p.say("Query cost: Dry running 2/4")
	p.say("Column impact: Looking up readers 1/2")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
}

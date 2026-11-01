package dbt

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/testutil"
)

func TestRunArgs(t *testing.T) {
	one := []project.ActionID{"p.s.orders"}
	name := []string{"shop.marts.orders"}
	tests := []struct {
		name   string
		target string
		opts   project.RunOptions
		want   string
	}{
		{"action only", "", project.RunOptions{Actions: one, Names: name}, "dbt build --select shop.marts.orders"},
		{"deps", "", project.RunOptions{Actions: one, Names: name, IncludeDeps: true}, "dbt build --select +shop.marts.orders"},
		{"dependents", "", project.RunOptions{Actions: one, Names: name, IncludeDependents: true}, "dbt build --select shop.marts.orders+"},
		{"full refresh with target", "ci", project.RunOptions{Actions: one, Names: name, IncludeDeps: true, IncludeDependents: true, FullRefresh: true},
			"dbt build --select +shop.marts.orders+ --full-refresh --target ci"},
		{"several actions", "", project.RunOptions{Actions: []project.ActionID{"a", "b"}, Names: []string{"shop.a", "shop.v.b.v2"}, IncludeDependents: true},
			"dbt build --select shop.a+ shop.v.b.v2+"},
		{"tags", "ci", project.RunOptions{Tags: []string{"daily", "hourly"}, IncludeDeps: true},
			"dbt build --select +tag:daily +tag:hourly --target ci"},
	}
	// The dbt on PATH is the one run, so the command names it as dbt.
	bin := testutil.FakeCLI(t, "dbt")
	t.Setenv("PATH", filepath.Dir(bin))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(bin, tt.target, nil, false)
			if got := b.RunCommand(t.TempDir(), tt.opts); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestStartRun(t *testing.T) {
	for _, tt := range []struct {
		exit string
		want int
	}{{"0", 0}, {"1", 1}} {
		t.Run("exit "+tt.exit, func(t *testing.T) {
			t.Setenv("FAKE_DBT_BUILD_EXIT", tt.exit)
			root := t.TempDir()
			b := New(testutil.FakeCLI(t, "dbt"), "ci", nil, false)
			r, err := b.StartRun(context.Background(), root, project.RunOptions{Actions: []project.ActionID{"p.s.t"}, Names: []string{"shop.marts.t"}, IncludeDeps: true})
			if err != nil {
				t.Fatal(err)
			}
			if r.Cmdline != "dbt build --select +shop.marts.t --target ci" {
				t.Errorf("cmdline = %q", r.Cmdline)
			}
			var lines []string
			for l := range r.Lines {
				lines = append(lines, l)
			}
			// In the project root, where dbt writes its own target/ and logs/,
			// and without colour.
			want := []string{"args: build --select +shop.marts.t --target ci", "dir: " + root, "1 of 1 OK", "Done."}
			if !slices.Equal(lines, want) {
				t.Errorf("lines = %q, want %q", lines, want)
			}
			select {
			case res := <-r.Done:
				if res.ExitCode != tt.want || res.Err != nil || res.Canceled {
					t.Errorf("result = %+v, want exit %d", res, tt.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no result")
			}
		})
	}
}

// A run needs every action's dbt name: without one dbt build would select
// nothing, or everything.
func TestStartRunWithoutNames(t *testing.T) {
	b := New(testutil.FakeCLI(t, "dbt"), "", nil, false)
	for _, o := range []project.RunOptions{
		{Actions: []project.ActionID{"p.s.t"}},
		{Actions: []project.ActionID{"p.s.a", "p.s.b"}, Names: []string{"shop.a"}},
	} {
		if r, err := b.StartRun(context.Background(), t.TempDir(), o); err == nil {
			t.Errorf("%+v: started %q, want an error", o, r.Cmdline)
		}
	}
}

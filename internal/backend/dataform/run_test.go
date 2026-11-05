package dataform

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/testutil"
)

func TestRunOptionsArgs(t *testing.T) {
	tests := []struct {
		name string
		opts project.RunOptions
		want []string
	}{
		{"action only", project.RunOptions{Actions: []project.ActionID{"p.s.t"}}, []string{"run", "--actions", "p.s.t"}},
		{"deps", project.RunOptions{Actions: []project.ActionID{"p.s.t"}, IncludeDeps: true}, []string{"run", "--actions", "p.s.t", "--include-deps"}},
		{"dependents", project.RunOptions{Actions: []project.ActionID{"p.s.t"}, IncludeDependents: true}, []string{"run", "--actions", "p.s.t", "--include-dependents"}},
		{"full refresh", project.RunOptions{Actions: []project.ActionID{"p.s.t"}, FullRefresh: true}, []string{"run", "--actions", "p.s.t", "--full-refresh"}},
		{"all flags", project.RunOptions{Actions: []project.ActionID{"p.s.t"}, IncludeDeps: true, IncludeDependents: true, FullRefresh: true},
			[]string{"run", "--actions", "p.s.t", "--include-deps", "--include-dependents", "--full-refresh"}},
		{"several actions", project.RunOptions{Actions: []project.ActionID{"p.s.a", "p.s.b"}, IncludeDependents: true},
			[]string{"run", "--actions", "p.s.a", "p.s.b", "--include-dependents"}},
		{"job prefix", project.RunOptions{Actions: []project.ActionID{"p.s.t"}, FullRefresh: true, JobPrefix: "xf-ab12"},
			[]string{"run", "--actions", "p.s.t", "--full-refresh", "--job-prefix", "xf-ab12"}},
		{"tags", project.RunOptions{Tags: []string{"daily", "hourly"}, IncludeDeps: true, FullRefresh: true},
			[]string{"run", "--tags", "daily", "hourly", "--include-deps", "--full-refresh"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RunArgs(tt.opts); !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestXfRunArgsGoAsGiven(t *testing.T) {
	b := Backend{Names: Names{SchemaSuffix: "dev", TablePrefix: "ax"}}
	o := project.RunOptions{Actions: []project.ActionID{"p.s.t"}, Args: []string{"--actions", "t", "--tags", "x", "--table-prefix=mine"}, JobPrefix: "xf-1"}
	want := []string{"run", "--actions", "t", "--tags", "x", "--table-prefix=mine", "--job-prefix", "xf-1", "--schema-suffix=dev"}
	if got := b.runArgs(o); !slices.Equal(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	o.Args = []string{"--job-prefix", "agent"}
	if got := b.runArgs(o); slices.Contains(got, "xf-1") {
		t.Errorf("the run's own --job-prefix stands: %q", got)
	}
}

type runOutcome struct {
	lines []string
	res   project.RunResult
}

func collect(t *testing.T, r *project.Run, timeout time.Duration) runOutcome {
	t.Helper()
	var out runOutcome
	deadline := time.After(timeout)
	for {
		select {
		case l, ok := <-r.Lines:
			if !ok {
				select {
				case out.res = <-r.Done:
					return out
				case <-deadline:
					t.Fatal("no result after lines closed")
				}
			}
			out.lines = append(out.lines, l)
		case <-deadline:
			t.Fatalf("run did not finish; lines so far: %q", out.lines)
		}
	}
}

func TestStartRun(t *testing.T) {
	bin := testutil.FakeCLI(t, "dataform")
	tests := []struct {
		name     string
		exit     string
		wantExit int
	}{
		{"success", "0", 0},
		{"failure", "3", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKE_RUN_EXIT", tt.exit)
			t.Setenv("FAKE_RUN_SLEEP", "0")
			r, err := StartRun(context.Background(), bin, t.TempDir(), project.RunOptions{Actions: []project.ActionID{"p.s.t"}, IncludeDeps: true})
			if err != nil {
				t.Fatal(err)
			}
			if r.Cmdline != "dataform run --actions p.s.t --include-deps" {
				t.Errorf("cmdline = %q", r.Cmdline)
			}
			out := collect(t, r, 5*time.Second)
			want := []string{"args: run --actions p.s.t --include-deps", "Compiling...", "step 1", "step 2", "step 3", "error line"}
			if !slices.Equal(out.lines, want) {
				t.Errorf("lines = %q, want %q", out.lines, want)
			}
			for _, l := range out.lines {
				if strings.Contains(l, "\x1b") {
					t.Errorf("ANSI not stripped: %q", l)
				}
			}
			if out.res.ExitCode != tt.wantExit || out.res.Canceled || out.res.Err != nil {
				t.Errorf("result = %+v, want exit %d", out.res, tt.wantExit)
			}
		})
	}
}

func TestStartRunCancel(t *testing.T) {
	bin := testutil.FakeCLI(t, "dataform")
	t.Setenv("FAKE_RUN_SLEEP", "10")
	// A child that shares the run's output: the cancel must reach it too, or
	// it holds the output open and the run never ends.
	t.Setenv("FAKE_RUN_GRANDCHILD", "1")
	ctx, cancel := context.WithCancel(context.Background())
	r, err := StartRun(ctx, bin, t.TempDir(), project.RunOptions{Actions: []project.ActionID{"p.s.t"}})
	if err != nil {
		t.Fatal(err)
	}
	for l := range r.Lines {
		if l == "Compiling..." {
			break
		}
	}
	start := time.Now()
	cancel()
	out := collect(t, r, 4*time.Second)
	if !out.res.Canceled {
		t.Errorf("result = %+v, want canceled", out.res)
	}
	if !slices.Contains(out.lines, "interrupted") {
		t.Errorf("the run wasn't asked to stop; lines = %q", out.lines)
	}
	if out.res.ExitCode != 130 {
		t.Errorf("exit = %d, want the tool's own 130 after it stopped", out.res.ExitCode)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("cancel took %s; the run's child should have stopped too", time.Since(start))
	}
}

// TestStartRunKill covers a run that doesn't stop when asked (dataform waits
// for its BigQuery jobs to cancel): it keeps running until Kill, which stops
// its whole process tree.
func TestStartRunKill(t *testing.T) {
	bin := testutil.FakeCLI(t, "dataform")
	t.Setenv("FAKE_RUN_SLEEP", "10")
	t.Setenv("FAKE_RUN_SLOW_CANCEL", "1")
	t.Setenv("FAKE_RUN_GRANDCHILD", "1")
	ctx, cancel := context.WithCancel(context.Background())
	r, err := StartRun(ctx, bin, t.TempDir(), project.RunOptions{Actions: []project.ActionID{"p.s.t"}})
	if err != nil {
		t.Fatal(err)
	}
	for l := range r.Lines {
		if l == "Compiling..." {
			break
		}
	}
	cancel()
	select {
	case l := <-r.Lines:
		if l != "cancelling" {
			t.Fatalf("got %q, want the run to acknowledge the cancel", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the run wasn't asked to stop")
	}
	// Still running a moment later: nothing kills it automatically.
	select {
	case res := <-r.Done:
		t.Fatalf("run ended on its own after the cancel: %+v", res)
	case <-time.After(300 * time.Millisecond):
	}
	start := time.Now()
	r.Kill()
	out := collect(t, r, 3*time.Second)
	if !out.res.Canceled || out.res.ExitCode == 0 {
		t.Errorf("result = %+v, want canceled with a failing exit", out.res)
	}
	if time.Since(start) > time.Second {
		t.Errorf("kill took %s; the run's child should have been killed too", time.Since(start))
	}
}

func TestStartRunMissingBinary(t *testing.T) {
	if _, err := StartRun(context.Background(), "/nope/dataform", t.TempDir(), project.RunOptions{Actions: []project.ActionID{"a.b"}}); err == nil {
		t.Error("want error for a missing binary")
	}
}

// TestJobTarget: where the CLI runs its jobs is in the project's
// credentials file, when it has one.
func TestJobTarget(t *testing.T) {
	root := t.TempDir()
	if p, l := JobTarget(root); p != "" || l != "" {
		t.Errorf("no file: %q %q", p, l)
	}
	if err := os.WriteFile(filepath.Join(root, ".df-credentials.json"), []byte(`{"projectId": "billing", "location": "EU"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, l := JobTarget(root); p != "billing" || l != "EU" {
		t.Errorf("got %q %q", p, l)
	}
}

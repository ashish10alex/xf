//go:build darwin || linux

package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
)

// A project opened without its CLI says in the errors tab how to install
// it, runs that in the terminal tab when asked, and compiles again once
// the CLI can be found, back at the shell's prompt.
func TestInstallTheMissingCLIInTheTerminal(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	var found atomic.Bool
	eng := newFakeEngine(testGraph())
	install := &InstallCLI{Name: "dataform", Command: "echo installed-$((1+1))", Found: found.Load}
	s, ts, c := newTestProject(t, &Project{Deps: frontend.Deps{Engine: eng, Root: t.TempDir()}, InstallCLI: install})
	p := s.only()
	eng.states <- engine.State{Graph: p.graph(), Compiles: 2, Stale: true, Err: errors.New(`could not find "dataform" on PATH`)}
	deadline := time.Now().Add(5 * time.Second)
	for !p.hasErrors() {
		if time.Now().After(deadline) {
			t.Fatal("the failed compile never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var v struct {
		CompileErr string
		Fix        *fix
	}
	getJSON(t, c, ts.URL+"/api/errors", &v)
	if v.CompileErr == "" || v.Fix == nil || v.Fix.Command != install.Command {
		t.Fatalf("errors = %+v, want the compile's error and the install command", v)
	}

	_, ch, err := p.terminal().subscribe()
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	postJSON(t, c, ts.URL+"/api/term/fix", struct{}{}, nil)
	waitForTerm(t, ch, &seen, "installed-2")
	recompiles := func() int {
		eng.mu.Lock()
		defer eng.mu.Unlock()
		return eng.recompiles
	}
	if n := recompiles(); n != 0 {
		t.Fatalf("recompiled %d times while the CLI can't be found", n)
	}

	found.Store(true)
	_ = p.terminal().input([]byte("echo back-$((2+2))\r"))
	waitForTerm(t, ch, &seen, "back-4")
	for recompiles() != 1 {
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatalf("recompiled %d times once the CLI was found, want 1", recompiles())
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = p.terminal().input([]byte("echo again\r"))
	waitForTerm(t, ch, &seen, "again")
	time.Sleep(50 * time.Millisecond)
	if n := recompiles(); n != 1 {
		t.Errorf("recompiled %d times, want once", n)
	}
	v.Fix = nil // left out when there is none
	getJSON(t, c, ts.URL+"/api/errors", &v)
	if v.Fix != nil {
		t.Errorf("the fix %+v is still offered once the CLI is found", v.Fix)
	}
}

// A compile that fails because the project's @dataform/core is missing or
// doesn't match the CLI offers dataform install, with the CLI the compile
// ran, and compiles again once it has installed @dataform/core.
func TestDataformInstallInTheTerminal(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"dependencies":{"@dataform/core":"3.0.71"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dataform whose install writes @dataform/core's package.json.
	bin := filepath.Join(t.TempDir(), "my dataform")
	script := "#!/bin/sh\nmkdir -p node_modules/@dataform/core && echo '{\"version\":\"3.0.71\"}' > node_modules/@dataform/core/package.json && echo core-installed\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	eng := newFakeEngine(testGraph())
	s, ts, c := newTestProject(t, &Project{Deps: frontend.Deps{Engine: eng, Root: root, Info: &frontend.ProjectInfo{ToolBin: bin}}})
	p := s.only()
	eng.states <- engine.State{Graph: p.graph(), Compiles: 2, Stale: true, Err: errors.New("dataform compile --json: exit status 1\n" +
		"Error: @dataform/core 0.0.0 is not compatible with @dataform/cli 3.1.2. The CLI requires @dataform/core >=3.0.0 <4.0.0. " +
		"Set `dataformCoreVersion: 3.1.2` in workflow_settings.yaml, or pin @dataform/core in package.json and run `dataform install`.")}
	deadline := time.Now().Add(5 * time.Second)
	for !p.hasErrors() {
		if time.Now().After(deadline) {
			t.Fatal("the failed compile never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var v struct{ Fix *fix }
	getJSON(t, c, ts.URL+"/api/errors", &v)
	if want := "'" + bin + "' install"; v.Fix == nil || v.Fix.Command != want {
		t.Fatalf("fix = %+v, want the command %q", v.Fix, want)
	}

	_, ch, err := p.terminal().subscribe()
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	postJSON(t, c, ts.URL+"/api/term/fix", struct{}{}, nil)
	waitForTerm(t, ch, &seen, "core-installed")
	recompiles := func() int {
		eng.mu.Lock()
		defer eng.mu.Unlock()
		return eng.recompiles
	}
	for recompiles() != 1 {
		if time.Now().After(deadline.Add(5 * time.Second)) {
			t.Fatalf("recompiled %d times once dataform install was done, want 1", recompiles())
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = p.terminal().input([]byte("echo again\r"))
	waitForTerm(t, ch, &seen, "again")
	time.Sleep(50 * time.Millisecond)
	if n := recompiles(); n != 1 {
		t.Errorf("recompiled %d times, want once", n)
	}
}

// A dataform run that fails for want of .df-credentials.json offers
// dataform init-creds, with the dataform the compile ran, at the end of
// its log, and runs it in the terminal tab when asked.
func TestInitCredsAfterARunWithoutCredentials(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	bin := filepath.Join(t.TempDir(), "dataform")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"creds-$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := make(chan *fakeRun, 1)
	start := func(ctx context.Context, o project.RunOptions) (*project.Run, error) {
		f := &fakeRun{lines: make(chan string), done: make(chan project.RunResult, 1), ctx: ctx, opts: o}
		started <- f
		return &project.Run{Lines: f.lines, Done: f.done}, nil
	}
	s, ts, c := newTest(t, frontend.Deps{Engine: newFakeEngine(testGraph()), StartRun: start, Root: t.TempDir(),
		Info: &frontend.ProjectInfo{ToolBin: bin}, RunCommand: func(o project.RunOptions) string { return "dataform run" }})
	p := s.only()
	postJSON(t, c, ts.URL+"/api/run", runRequest{Kind: "actions", IDs: []project.ActionID{"p.mart.orders"}}, nil)
	f := <-started
	f.lines <- "Dataform encountered an error: Missing credentials JSON file; not found at path '/work/p/.df-credentials.json'."
	close(f.lines)
	f.done <- project.RunResult{ExitCode: 1, Duration: time.Second}
	var v runView
	waitFor(t, func() bool {
		getJSON(t, c, ts.URL+"/api/run", &v)
		return !v.Running && v.Title != "running…"
	})
	last := v.Lines[len(v.Lines)-1]
	if last.Fix == nil || last.Fix.Command != bin+" init-creds" {
		t.Fatalf("last line = %+v, want dataform init-creds offered", last)
	}

	_, ch, err := p.terminal().subscribe()
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	postJSON(t, c, ts.URL+"/api/term/fix", map[string]bool{"run": true}, nil)
	waitForTerm(t, ch, &seen, "creds-init-creds")
}

// The command isn't typed into a command running in the terminal.
func TestTerminalRunWaitsForThePrompt(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	term := newTerminal(t.TempDir(), nil, nil)
	defer term.close()
	_, ch, err := term.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	_ = term.input([]byte("echo ready\r"))
	waitForTerm(t, ch, &seen, "ready")
	_ = term.input([]byte("sleep 30\r"))
	deadline := time.Now().Add(5 * time.Second)
	for term.command() != "sleep" {
		if time.Now().After(deadline) {
			t.Fatalf("the command is %q, want sleep", term.command())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := term.run("echo no"); err == nil || !strings.Contains(err.Error(), "sleep") {
		t.Errorf("run while sleep runs: %v, want an error naming it", err)
	}
}

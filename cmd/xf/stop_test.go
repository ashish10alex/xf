package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/testutil"
)

func TestXfRole(t *testing.T) {
	bin := filepath.Join("C:", "Users", "me", "AppData", "Local", "xf", "bin")
	copyPath := filepath.Join(bin, "xf-0123456789abcdef.exe")
	for _, tc := range []struct {
		name string
		p    proc
		want string
	}{
		{"engine", proc{Name: "xf", Mine: true, Args: []string{"xf", "serve", "--dir", "/p"}}, roleServe},
		{"lsp", proc{Name: "xf", Mine: true, Args: []string{"/usr/local/bin/xf", "lsp"}}, roleLSP},
		{"tui", proc{Name: "xf", Mine: true, Args: []string{"xf"}}, roleTUI},
		{"tui with flags", proc{Name: "xf", Mine: true, Args: []string{"xf", "--dir", "x"}}, roleTUI},
		{"other command", proc{Name: "xf", Mine: true, Args: []string{"xf", "config"}}, "config"},
		{"ctrl-c helper", proc{Name: "xf", Mine: true, Args: []string{"xf", "__ctrlc", "12"}}, ""},
		{"another user's", proc{Name: "xf", Args: []string{"xf", "serve"}}, ""},
		{"not xf", proc{Name: "xfoo", Mine: true, Args: []string{"xfoo", "serve"}}, ""},
		{"engine copy", proc{Name: "xf-0123456789abcdef", Path: copyPath, Mine: true, Args: []string{copyPath, "serve"}}, roleServe},
		{"engine copy, no command line", proc{Name: "xf-0123456789abcdef", Path: copyPath, Mine: true}, roleServe},
		{"xf- elsewhere", proc{Name: "xf-tool", Path: filepath.Join("C:", "tools", "xf-tool.exe"), Mine: true, Args: []string{"xf-tool", "serve"}}, ""},
	} {
		if got := xfRole(tc.p, bin); got != tc.want {
			t.Errorf("%s: role %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAppOf(t *testing.T) {
	byPID := map[int]proc{}
	for _, p := range []proc{
		{PID: 1, PPID: 0, Name: "launchd"},
		{PID: 10, PPID: 1, Name: "iTerm2"},
		{PID: 11, PPID: 10, Name: "login"},
		{PID: 12, PPID: 11, Name: "zsh"},
		{PID: 13, PPID: 12, Name: "xf", Mine: true},
		{PID: 20, PPID: 1, Name: "nvim"},
		{PID: 21, PPID: 20, Name: "xf", Mine: true},
		{PID: 30, PPID: 31, Name: "pwsh"},
		{PID: 32, PPID: 30, Name: "xf", Mine: true},
	} {
		byPID[p.PID] = p
	}
	for _, tc := range []struct {
		pid  int
		app  string
		apid int
	}{
		{13, "iTerm2", 10},
		{21, "nvim", 20},
		{32, "", 0}, // its shell's parent is gone
	} {
		app, pid := appOf(byPID[tc.pid], byPID)
		if app != tc.app || pid != tc.apid {
			t.Errorf("app of %d = %q (%d), want %q (%d)", tc.pid, app, pid, tc.app, tc.apid)
		}
	}
	// A loop in the parents ends.
	loop := map[int]proc{1: {PID: 1, PPID: 2, Name: "sh"}, 2: {PID: 2, PPID: 1, Name: "sh"}}
	if app, _ := appOf(proc{PID: 3, PPID: 1}, loop); app != "" {
		t.Errorf("app in a loop = %q", app)
	}
}

func TestPrintRemaining(t *testing.T) {
	procs := []proc{
		{PID: 20, PPID: 1, Name: "nvim"},
		{PID: 21, PPID: 20, Name: "xf", Mine: true, Args: []string{"xf", "lsp"}},
		{PID: 22, PPID: 1, Name: "xf", Mine: true, Args: []string{"xf", "serve"}},
		{PID: 23, PPID: 1, Name: "xf", Mine: true, Args: []string{"xf", "stop"}},
	}
	var out bytes.Buffer
	printRemaining(&out, procs, "", 23, "windows")
	got := out.String()
	if !strings.Contains(got, "xf lsp   pid 21      in nvim (pid 20)") || strings.Contains(got, "pid 22") || strings.Contains(got, "pid 23") {
		t.Errorf("unexpected listing:\n%s", got)
	}
	if !strings.Contains(got, "xf.exe") {
		t.Errorf("no advice for Windows:\n%s", got)
	}
	out.Reset()
	printRemaining(&out, procs[2:], "", 23, "windows")
	if out.Len() != 0 {
		t.Errorf("listed engines or self:\n%s", out.String())
	}
}

func TestArgValue(t *testing.T) {
	args := []string{"xf", "serve", "--dir", `C:\my project`, "--backend=dbt"}
	if got := argValue(args, "dir"); got != `C:\my project` {
		t.Errorf("dir = %q", got)
	}
	if got := argValue(args, "backend"); got != "dbt" {
		t.Errorf("backend = %q", got)
	}
	if got := argValue(args, "nope"); got != "" {
		t.Errorf("nope = %q", got)
	}
}

// listProcs finds this test, with its parent, path and arguments.
func TestListProcsFindsItself(t *testing.T) {
	procs, err := listProcs()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(procs, func(p proc) bool { return p.PID == os.Getpid() })
	if i < 0 {
		t.Fatalf("this process (%d) isn't among %d", os.Getpid(), len(procs))
	}
	p := procs[i]
	exe, _ := os.Executable()
	if !p.Mine || p.PPID != os.Getppid() || !strings.EqualFold(filepath.Base(p.Path), filepath.Base(exe)) || !slices.Equal(p.Args, os.Args) {
		t.Errorf("this process is %+v; want parent %d, path %s, args %q", p, os.Getppid(), exe, os.Args)
	}
	if !slices.ContainsFunc(procs, func(q proc) bool { return q.PID == p.PPID }) {
		t.Errorf("the parent %d isn't listed", p.PPID)
	}
}

func TestStopProc(t *testing.T) {
	cmd := exec.Command(testutil.FakeCLI(t, "xf-wait"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	if err := stopProc(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the process is still running")
	}
}

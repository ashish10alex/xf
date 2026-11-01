package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// proc is a running process, as xf stop sees it.
type proc struct {
	PID, PPID int
	// Name is the executable's base name, without .exe.
	Name string
	// Path is the executable's path, and Args its command line; both are
	// empty when they can't be read (another user's process, say).
	Path string
	Args []string
	// Mine says whether the current user owns it.
	Mine bool
}

// xf processes' roles.
const (
	roleServe = "serve"
	roleLSP   = "lsp"
	roleTUI   = "tui"
)

// xfRole says what p is if it is an xf of this user's: roleServe, roleLSP,
// roleTUI or another subcommand's name; "" when it isn't one. binDir is
// where engines run from copies of xf (see engineBinDir).
func xfRole(p proc, binDir string) string {
	if !p.Mine {
		return ""
	}
	isCopy := binDir != "" && p.Path != "" && strings.EqualFold(filepath.Dir(p.Path), binDir) && strings.HasPrefix(p.Name, engineCopyPrefix)
	if p.Name != "xf" && !isCopy {
		return ""
	}
	if len(p.Args) < 2 {
		if isCopy {
			return roleServe
		}
		return roleTUI
	}
	switch arg := p.Args[1]; {
	case arg == "serve" || arg == "lsp":
		return arg
	case arg == "__ctrlc":
		return "" // the helper a cancel runs for a moment
	case strings.HasPrefix(arg, "-"):
		return roleTUI
	default:
		return arg
	}
}

// launchers are programs that only start others: a process's app is its
// nearest ancestor that isn't one.
var launchers = []string{
	"sh", "bash", "zsh", "fish", "dash", "ksh", "tcsh", "csh", "nu", "login", "sudo", "su", "env",
	"pwsh", "powershell", "cmd", "conhost", "openconsole", "xf",
}

// appOf names the app that p runs in (an editor, a terminal), with its
// PID: its nearest ancestor that isn't a shell or the like. It is "" when
// the chain ends first.
func appOf(p proc, byPID map[int]proc) (string, int) {
	seen := map[int]bool{p.PID: true}
	for pid := p.PPID; pid > 0 && !seen[pid]; {
		seen[pid] = true
		parent, ok := byPID[pid]
		if !ok {
			return "", 0
		}
		if !slices.Contains(launchers, strings.ToLower(parent.Name)) {
			return parent.Name, parent.PID
		}
		pid = parent.PPID
	}
	return "", 0
}

// argValue is the value of the flag --name in args, or "".
func argValue(args []string, name string) string {
	for i, a := range args {
		if a == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			return v
		}
	}
	return ""
}

// runStop stops every xf serve of the current user, then lists the other
// xf processes still running, which hold xf's executable open on Windows,
// so they can be closed before an upgrade. It doesn't stop those: an
// editor would only start its language server again.
func runStop(args []string) error {
	fs := flag.NewFlagSet("xf stop", flag.ContinueOnError)
	commandUsage(fs, "xf stop", "Stops every xf engine (xf serve) you are running, then lists the xf processes\nstill running (the TUI, xf lsp) and the app each runs in. On Windows those\nkeep xf.exe from being replaced: close them before upgrading xf.")
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v (see xf stop --help)", fs.Args())
	}
	procs, err := listProcs()
	if err != nil {
		return fmt.Errorf("listing processes: %w", err)
	}
	binDir, _ := engineBinDir()
	stopped, failed := 0, 0
	for _, p := range procs {
		if p.PID == os.Getpid() || xfRole(p, binDir) != roleServe {
			continue
		}
		if err := stopProc(p.PID); err != nil {
			fmt.Fprintf(os.Stderr, "xf: couldn't stop xf serve (pid %d): %v\n", p.PID, err)
			failed++
			continue
		}
		fmt.Printf("stopped xf serve (pid %d)%s\n", p.PID, forProject(p))
		stopped++
	}
	if stopped == 0 && failed == 0 {
		fmt.Println("no xf engines were running")
	}
	// Listed afterwards, so an xf that has just exited isn't.
	if procs, err = listProcs(); err != nil {
		return fmt.Errorf("listing processes: %w", err)
	}
	printRemaining(os.Stdout, procs, binDir, os.Getpid(), runtime.GOOS)
	if failed > 0 {
		return fmt.Errorf("%d engines are still running", failed)
	}
	return nil
}

// forProject says which project an xf serve with p's command line serves.
func forProject(p proc) string {
	if dir := argValue(p.Args, "dir"); dir != "" {
		return " for " + dir
	}
	return ""
}

// printRemaining lists the xf processes in procs other than engines and
// self, with the app each runs in, and what they mean for an upgrade on
// goos.
func printRemaining(w io.Writer, procs []proc, binDir string, self int, goos string) {
	byPID := map[int]proc{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	var lines []string
	for _, p := range procs {
		role := xfRole(p, binDir)
		if p.PID == self || role == "" || role == roleServe {
			continue
		}
		what := "xf " + role
		if role == roleTUI {
			what = "xf"
		}
		line := fmt.Sprintf("  %-8s pid %-7d", what, p.PID)
		if app, pid := appOf(p, byPID); app != "" {
			line += fmt.Sprintf(" in %s (pid %d)", app, pid)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "\nstill running:\n%s\n\n", strings.Join(lines, "\n"))
	if goos == "windows" {
		_, _ = fmt.Fprintln(w, "These keep xf.exe from being replaced: quit xf, and close the editor windows\nthat run xf lsp, before upgrading xf. The engines they start again run from a\ncopy of xf, which doesn't.")
	} else {
		_, _ = fmt.Fprintln(w, "These run the xf build they started with until they are restarted.")
	}
}

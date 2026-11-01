// Package cmdrun runs a tool's CLI for a run of a project's actions,
// streaming its output, as the dataform and dbt backends do.
package cmdrun

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

// Start starts bin with args in root and streams its combined stdout and
// stderr line by line, with ANSI escape codes removed. name is how the
// run's Cmdline names the tool ("dataform", "dbt"), rather than by bin's
// path. With color, the tool is asked for colour (see ForceColor) and
// the lines keep their colour codes.
//
// Cancelling ctx asks the run's processes to stop: SIGINT to its process
// group on Unix, a console Ctrl+C on Windows. Tools handle it by cancelling their BigQuery jobs and
// waiting for them, which can take a while, so nothing is killed
// automatically: call Run.Kill to force it. Kill and the end of the run
// stop the run's whole process tree.
func Start(ctx context.Context, bin, name, root string, args []string, color bool) (*project.Run, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = root
	isolate(cmd)
	clean := StripANSI
	if color {
		cmd.Env = ForceColor(os.Environ())
		clean = keepColor
	}

	// An OS pipe (rather than an io.Writer) is handed to the child directly,
	// so Wait doesn't wait on a copy goroutine that a lingering worker could
	// hold open. The reader sees EOF once every process in the group is gone.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	start := time.Now()
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, err
	}
	_ = pw.Close() // the child has its own copy
	tree, err := attach(cmd.Process)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = pr.Close()
		return nil, err
	}

	exited := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			tree.interrupt()
		case <-exited:
		}
	}()

	lines := make(chan string)
	done := make(chan project.RunResult, 1)

	go func() {
		err := cmd.Wait()
		close(exited)
		// Don't leave workers behind once the main process has exited.
		tree.kill()
		res := project.RunResult{Duration: time.Since(start), Canceled: ctx.Err() != nil}
		var exitErr *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &exitErr):
			res.ExitCode = exitErr.ExitCode()
			if res.ExitCode == exitCanceled {
				res.ExitCode = 130 // what a ctrl+c'd process exits with on Unix
			}
			if res.ExitCode == -1 && !res.Canceled {
				res.Err = err // killed by a signal we didn't send
			}
		default:
			res.ExitCode = -1
			res.Err = err
		}
		done <- res
	}()

	go func() {
		defer close(lines)
		defer func() { _ = pr.Close() }()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			lines <- clean(strings.TrimRight(sc.Text(), "\r"))
		}
		// Drain after a scan error (e.g. an over-long line) so the child
		// never blocks writing to a full pipe.
		_, _ = io.Copy(io.Discard, pr)
	}()

	return &project.Run{
		Cmdline: ShellJoin(append([]string{name}, args...)),
		Lines:   lines,
		Done:    done,
		Kill:    tree.kill,
	}, nil
}

// ansiRE matches CSI and OSC escape sequences (colours, cursor movement, links).
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// StripANSI removes ANSI escape sequences from s.
func StripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// keepColor removes the ANSI escape sequences from s but its colour codes
// (SGR: ESC [ ... m).
func keepColor(s string) string {
	return ansiRE.ReplaceAllStringFunc(s, func(seq string) string {
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") && !strings.Contains(seq, "?") {
			return seq
		}
		return ""
	})
}

// ForceColor is env asking a tool for colour when its output is a pipe:
// FORCE_COLOR=1, which Dataform's CLI and dbt read. A FORCE_COLOR or
// NO_COLOR already set is the user's choice and left as it is.
func ForceColor(env []string) []string {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); (k == "FORCE_COLOR" || k == "NO_COLOR") && v != "" {
			return env
		}
	}
	return append(env, "FORCE_COLOR=1")
}

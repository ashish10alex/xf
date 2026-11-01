package cmdrun

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/testutil"
)

// Cancelling a Command stops its whole process tree: a child that shares
// its output can't keep Wait waiting.
func TestCommandCancelStopsTheTree(t *testing.T) {
	t.Setenv("FAKE_RUN_SLEEP", "10")
	t.Setenv("FAKE_RUN_GRANDCHILD", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := Command(ctx, testutil.FakeCLI(t, "dataform"), "run")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() && sc.Text() != "\x1b[32mCompiling...\x1b[0m" {
	}
	start := time.Now()
	cancel()
	for sc.Scan() {
	}
	err = cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) || time.Since(start) > time.Second {
		t.Errorf("Wait = %v after %s: the tree outlived the cancel", err, time.Since(start))
	}
}

// Output runs a Command and, as exec.Cmd does, keeps a failure's stderr.
func TestCommandOutput(t *testing.T) {
	t.Setenv("FAKE_COMPILE_STDERR", "boom")
	t.Setenv("FAKE_COMPILE_EXIT", "3")
	_, err := Command(context.Background(), testutil.FakeCLI(t, "dataform"), "compile").Output()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 || string(exitErr.Stderr) != "boom\n" {
		t.Errorf("err = %v (%T)", err, err)
	}
}

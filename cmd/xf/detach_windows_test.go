package main

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/ashish10alex/xf/internal/testutil"
)

// Inside a Job Object that forbids breaking away (CI runners' do),
// startDetached still starts the process, detached only.
func TestStartDetachedInAJobThatForbidsBreakaway(t *testing.T) {
	if prog := os.Getenv("XF_TEST_DETACH_IN_JOB"); prog != "" {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
			t.Fatal(err)
		}
		cmd, err := startDetached(exec.Command(prog))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		_ = cmd.Wait()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStartDetachedInAJobThatForbidsBreakaway$", "-test.v")
	cmd.Env = append(os.Environ(), "XF_TEST_DETACH_IN_JOB="+testutil.FakeCLI(t, "xf-fail"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

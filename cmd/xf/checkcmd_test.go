package main

import (
	"testing"

	"github.com/ashish10alex/xf/internal/check"
)

func TestCheckRefusesBadFlagsBeforeLookingForAProject(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"--fail-on", "minor"},
		{"--fail-over", "lots"},
		{"--fail-over", "0"},
		{"--quiet-under", "some"},
		{"--usd-per-tib", "0"},
		{"--no-such-flag"},
		{"stray"},
	} {
		if code := runCheck(args); code != check.Failed {
			t.Errorf("xf check %v exited %d, want %d", args, code, check.Failed)
		}
	}
	if code := runCheck([]string{"--help"}); code != check.OK {
		t.Errorf("xf check --help exited %d", code)
	}
}

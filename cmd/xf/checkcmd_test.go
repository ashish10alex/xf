package main

import (
	"testing"

	"github.com/ashish10alex/xf/internal/check"
	"github.com/ashish10alex/xf/internal/impact"
)

func TestCheckRefusesBadFlagsBeforeLookingForAProject(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{
		{"--fail-on", "minor"},
		{"--fail-over", "lots"},
		{"--fail-over", "0"},
		{"--fail-over-delta", "lots"},
		{"--fail-over-delta", "0"},
		{"--quiet-under", "some"},
		{"--usd-per-tib", "0"},
		{"--usd-per-tib", "cheap"},
		{"--usd-per-tib", ""},
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

func TestParseFailOn(t *testing.T) {
	for _, tc := range []struct {
		in   string
		sev  impact.Severity
		gate bool
		err  bool
	}{
		{"", 0, false, false},
		{"critical", impact.SeverityCritical, true, false},
		{"warning", impact.SeverityWarning, true, false},
		{"unknown", impact.SeverityUnknown, true, false},
		{"minor", 0, false, true},
	} {
		sev, gate, err := parseFailOn(tc.in)
		if (err != nil) != tc.err || sev != tc.sev || gate != tc.gate {
			t.Errorf("parseFailOn(%q) = (%v, %v, %v), want (%v, %v, err=%v)", tc.in, sev, gate, err, tc.sev, tc.gate, tc.err)
		}
	}
}

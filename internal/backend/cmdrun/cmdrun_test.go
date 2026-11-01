package cmdrun

import (
	"slices"
	"testing"
)

func TestStripANSI(t *testing.T) {
	if got := StripANSI("\x1b[32mOK\x1b[0m \x1b]8;;http://x\x07link\x1b]8;;\x07"); got != "OK link" {
		t.Errorf("got %q", got)
	}
}

func TestKeepColor(t *testing.T) {
	in := "\x1b[?25l\x1b[32m\x1b[1mOK\x1b[0m \x1b[2K\x1b]8;;http://x\x07link\x1b]8;;\x07"
	if got, want := keepColor(in), "\x1b[32m\x1b[1mOK\x1b[0m link"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestForceColor(t *testing.T) {
	for _, tt := range []struct {
		env, want []string
	}{
		{[]string{"PATH=/bin"}, []string{"PATH=/bin", "FORCE_COLOR=1"}},
		{[]string{"NO_COLOR=1"}, []string{"NO_COLOR=1"}},
		{[]string{"FORCE_COLOR=0"}, []string{"FORCE_COLOR=0"}},
		{[]string{"NO_COLOR="}, []string{"NO_COLOR=", "FORCE_COLOR=1"}}, // empty is unset
	} {
		if got := ForceColor(slices.Clone(tt.env)); !slices.Equal(got, tt.want) {
			t.Errorf("ForceColor(%q) = %q, want %q", tt.env, got, tt.want)
		}
	}
}

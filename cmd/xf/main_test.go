package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Bare xf outside any project, with none remembered, succeeds with a hint:
// winget's validation runs it so. Asked for a folder that isn't a project,
// it still fails.
func TestBareXfOutsideAProject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(dir, "local"))
	t.Setenv("APPDATA", filepath.Join(dir, "roaming"))
	t.Chdir(dir)

	if err := runTUI(nil); err != nil {
		t.Errorf("bare xf: %v, want no error", err)
	}
	if err := runTUI([]string{"--dir", dir}); err == nil {
		t.Error("xf --dir on a folder that isn't a project: no error")
	}
}

func TestNoProjectHint(t *testing.T) {
	var b strings.Builder
	noProjectHint(&b)
	for _, want := range []string{"no Dataform or dbt project", "xf web", "xf search", "--help"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("hint lacks %q:\n%s", want, b.String())
		}
	}
}

func TestPiped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows terminals may give programs pipes")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	f, err := os.Create(filepath.Join(t.TempDir(), "in.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()

	if !piped(r) || !piped(f) {
		t.Error("a pipe or a file isn't piped")
	}
	if piped(null) {
		t.Error("/dev/null is piped") // a device, as a terminal is
	}
}

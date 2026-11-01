//go:build !windows

package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// countGit puts a git on PATH that logs each call before running the real
// one, and returns a func that reads how many calls there have been.
func countGit(t *testing.T) func() []string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// TestSourceLoadGitCalls: showing a file runs git once, to read its index
// version. Every git call is a process (more with core.fsmonitor, whose
// daemon a call can start), and the source pane loads a file on every
// selection, so a new call here is felt: it should be a decision.
func TestSourceLoadGitCalls(t *testing.T) {
	root, ts, c := newSourceTest(t, map[string]string{"definitions/orders.sqlx": "SELECT 1\n"})
	for _, args := range [][]string{{"init", "-q"}, {"add", "definitions/orders.sqlx"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	calls := countGit(t)

	var v sourceView
	getJSON(t, c, ts.URL+"/api/source?file=definitions/orders.sqlx", &v)
	if v.Base == nil || *v.Base != "SELECT 1\n" {
		t.Fatalf("base = %v, want the index's text", v.Base)
	}
	if got := calls(); len(got) != 1 {
		t.Errorf("loading a tracked file ran git %d times, want 1:\n%s", len(got), strings.Join(got, "\n"))
	}
}

// Loading a file again runs no git while the index is unchanged, and a
// file git doesn't track runs git twice: to find it isn't in the index,
// then that git doesn't ignore it.
func TestSourceReloadGitCalls(t *testing.T) {
	root, ts, c := newSourceTest(t, map[string]string{"definitions/orders.sqlx": "SELECT 1\n", "definitions/new.sqlx": "SELECT 2\n"})
	for _, args := range [][]string{{"init", "-q"}, {"add", "definitions/orders.sqlx"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	settleIndex(t, root)
	calls := countGit(t)

	var v sourceView
	for range 3 {
		getJSON(t, c, ts.URL+"/api/source?file=definitions/orders.sqlx", &v)
		if v.Base == nil || *v.Base != "SELECT 1\n" {
			t.Fatalf("base = %v, want the index's text", v.Base)
		}
	}
	if got := calls(); len(got) != 1 {
		t.Errorf("loading a tracked file 3 times ran git %d times, want 1:\n%s", len(got), strings.Join(got, "\n"))
	}
	getJSON(t, c, ts.URL+"/api/source?file=definitions/new.sqlx", &v)
	if v.Base == nil || *v.Base != "" {
		t.Fatalf("untracked base = %v, want empty", v.Base)
	}
	if got := calls(); len(got) != 3 {
		t.Errorf("loading an untracked file ran git %d times, want 2:\n%s", len(got)-1, strings.Join(got, "\n"))
	}
}

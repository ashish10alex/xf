package gitscm

import (
	"context"
	"path/filepath"
	"testing"
)

// BlameOf names each line's commit, from the text given rather than the
// file on disk, and "" for a line not committed.
func TestBlameOf(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "sub/a.sqlx", "one\ntwo\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "first")
	first := gitIn(t, dir, "rev-parse", "HEAD")
	write(t, dir, "sub/a.sqlx", "one\ntwo\nthree\n")
	gitIn(t, dir, "commit", "-q", "-am", "second commit")
	second := gitIn(t, dir, "rev-parse", "HEAD")

	// "TWO" is in the editor only, not saved.
	b, err := BlameOf(context.Background(), filepath.Join(dir, "sub", "a.sqlx"), []byte("one\nTWO\nthree\nfour\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{first, "", second, ""}
	if len(b.Lines) != len(want) {
		t.Fatalf("lines = %q, want %q", b.Lines, want)
	}
	for i := range want {
		if b.Lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i+1, b.Lines[i], want[i])
		}
	}
	c := b.Commits[second]
	if c.Summary != "second commit" || c.Author != "t" || c.Mail != "t@t" || c.Time == 0 {
		t.Errorf("second commit = %+v", c)
	}
	if b.Commits[first].Summary != "first" {
		t.Errorf("first commit = %+v", b.Commits[first])
	}
	if len(b.Commits) != 2 {
		t.Errorf("commits = %v, want 2", b.Commits)
	}

	write(t, dir, "new.sqlx", "x\n")
	if _, err := BlameOf(context.Background(), filepath.Join(dir, "new.sqlx"), []byte("x\n")); err == nil {
		t.Error("blame of an untracked file: no error")
	}
}

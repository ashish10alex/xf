package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopyEngine(t *testing.T) {
	src := filepath.Join(t.TempDir(), "xf.exe")
	built := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	writeExe := func(content string) {
		t.Helper()
		if err := os.WriteFile(src, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(src, built, built); err != nil {
			t.Fatal(err)
		}
	}
	writeExe("build one")
	dir := filepath.Join(t.TempDir(), "xf", "bin")

	first, err := copyEngine(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(first) != dir || !strings.HasPrefix(filepath.Base(first), engineCopyPrefix) || filepath.Ext(first) != ".exe" {
		t.Errorf("copy at %s", first)
	}
	if got, _ := os.ReadFile(first); !bytes.Equal(got, []byte("build one")) {
		t.Errorf("copy holds %q", got)
	}
	if fi, err := os.Stat(first); err != nil || !fi.ModTime().Equal(built) {
		t.Errorf("the copy's time isn't the build's: %v, %v", fi.ModTime(), err)
	}
	again, err := copyEngine(src, dir)
	if err != nil || again != first {
		t.Errorf("copying the same build again gave %s, %v", again, err)
	}

	writeExe("build two")
	second, err := copyEngine(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("a new build got the old build's copy")
	}
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cleanEngines(dir, second)
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("the old copy is still there: %v", err)
	}
	for _, keep := range []string{second, other} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was removed: %v", keep, err)
		}
	}
}

func TestReadBuild(t *testing.T) {
	b := readBuild()
	exe, _ := os.Executable()
	fi, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Time.Equal(fi.ModTime()) || b.Size != fi.Size() {
		t.Errorf("build %v; the executable is %v, %d bytes", b, fi.ModTime(), fi.Size())
	}
}

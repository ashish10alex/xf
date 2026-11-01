package trash

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFreeName(t *testing.T) {
	taken := map[string]bool{"a.sqlx": true, "a 2.sqlx": true, ".env": true, "dir": true}
	is := func(n string) bool { return taken[n] }
	for in, want := range map[string]string{"b.sqlx": "b.sqlx", "a.sqlx": "a 3.sqlx", ".env": ".env 2", "dir": "dir 2"} {
		if got := freeName(in, is); got != want {
			t.Errorf("freeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMoveUnix(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("the freedesktop trash")
	}
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	dir := t.TempDir()
	for range 2 {
		f := filepath.Join(dir, "my file.sqlx")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Move(f); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s is still there", f)
		}
	}
	for _, n := range []string{"my file.sqlx", "my file 2.sqlx"} {
		if _, err := os.Stat(filepath.Join(data, "Trash", "files", n)); err != nil {
			t.Error(err)
		}
		info, err := os.ReadFile(filepath.Join(data, "Trash", "info", n+".trashinfo"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(info), "Path="+filepath.ToSlash(dir)+"/my%20file.sqlx\n") || !strings.Contains(string(info), "DeletionDate=") {
			t.Errorf("info file:\n%s", info)
		}
	}
}

func TestMoveMissing(t *testing.T) {
	if err := Move(filepath.Join(t.TempDir(), "nope")); !os.IsNotExist(err) {
		t.Errorf("Move of a missing file: %v", err)
	}
}

func TestMoveDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the macOS Trash")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	for range 2 {
		d := filepath.Join(dir, "includes")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := Move(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"includes", "includes 2"} {
		if fi, err := os.Stat(filepath.Join(home, ".Trash", n)); err != nil || !fi.IsDir() {
			t.Errorf("%s in the Trash: %v", n, err)
		}
	}
}

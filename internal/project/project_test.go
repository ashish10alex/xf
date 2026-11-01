package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/testutil"
)

func TestTargetIDMatchesCLI(t *testing.T) {
	tests := []struct {
		target Target
		want   ActionID
	}{
		{Target{Database: "proj", Schema: "ds", Name: "tbl"}, "proj.ds.tbl"},
		{Target{Schema: "ds", Name: "tbl"}, "ds.tbl"},
	}
	for _, tt := range tests {
		if got := tt.target.ID(); got != tt.want {
			t.Errorf("%+v.ID() = %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestScript(t *testing.T) {
	stmts := []string{"  DECLARE x INT64 \n", "", "\n\tSELECT x;", "SELECT 2"}
	sql, parts := Script(stmts)
	if want := "DECLARE x INT64;\nSELECT x;\nSELECT 2;"; sql != want {
		t.Fatalf("script = %q, want %q", sql, want)
	}
	if len(parts) != 3 || parts[0].Stmt != 0 || parts[1].Stmt != 2 || parts[2].Stmt != 3 {
		t.Fatalf("parts = %+v", parts)
	}
	// An offset in the script maps back into the untrimmed statement.
	for _, p := range parts {
		for _, word := range []string{"DECLARE", "SELECT"} {
			i := strings.Index(sql[p.Start:], word)
			if i < 0 {
				continue
			}
			off := p.Start + i - p.Origin
			if got := stmts[p.Stmt][off : off+len(word)]; got != word {
				t.Errorf("part %+v maps %q to %q", p, word, got)
			}
			break
		}
	}
	if sql, _ := Script([]string{"  SELECT 1  "}); sql != "SELECT 1" {
		t.Errorf("single statement = %q", sql)
	}
}

func TestEditorEventFor(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "workflow_settings.yaml"), "")
	if err := os.MkdirAll(filepath.Join(root, "definitions", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := RealPath(root)
	write(t, filepath.Join(root, "definitions", "a.sqlx"), "select 1\n")
	link := filepath.Join(t.TempDir(), "link")
	testutil.Symlink(t, root, link)
	tests := []struct {
		event, file string
		want        EditorEvent
		ok          bool
	}{
		{"BufEnter", real + "/definitions/a.sqlx", EditorEvent{Event: "BufEnter", File: "definitions/a.sqlx"}, true},
		{"CursorHold", real + "/definitions/sub/b.js\n", EditorEvent{Event: "CursorHold", File: "definitions/sub/b.js"}, true},
		{"", real + "/workflow_settings.yaml", EditorEvent{Event: "BufEnter", File: "workflow_settings.yaml"}, true},
		{"BufEnter", link + "/definitions/a.sqlx", EditorEvent{Event: "BufEnter", File: "definitions/a.sqlx"}, true},
		{"BufEnter", "/other/definitions/a.sqlx", EditorEvent{}, false},
		{"BufEnter", real + "2/a.sqlx", EditorEvent{}, false},
		{"BufEnter", "relative/a.sqlx", EditorEvent{}, false},
		{"BufEnter", "", EditorEvent{}, false},
	}
	for _, tt := range tests {
		got, ok := EditorEventFor(root, tt.event, tt.file)
		if ok != tt.ok || got != tt.want {
			t.Errorf("EditorEventFor(%q, %q) = %+v, %v; want %+v, %v", tt.event, tt.file, got, ok, tt.want, tt.ok)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A Windows path names one engine and session whatever case its drive
// letter was typed in.
func TestCanonicalDrive(t *testing.T) {
	for in, want := range map[string]string{`c:\proj`: `C:\proj`, `C:\proj`: `C:\proj`, "/home/me": "/home/me", "c": "c", "": ""} {
		if got := CanonicalDrive(in); got != want {
			t.Errorf("CanonicalDrive(%q) = %q, want %q", in, got, want)
		}
	}
}

// On Windows a project root the tools can't work in is refused, saying what
// to do; nothing is refused elsewhere.
func TestCheckRoot(t *testing.T) {
	defer func(g string) { goos = g }(goos)
	long := `C:\` + strings.Repeat("x", MaxRootLen)
	goos = "windows"
	if err := CheckRoot(`C:\src\shop`); err != nil {
		t.Errorf("short root: %v", err)
	}
	if err := CheckRoot(long); err == nil || !strings.Contains(err.Error(), "somewhere shorter") {
		t.Errorf("long root: %v", err)
	}
	goos = "linux"
	if err := CheckRoot(long); err != nil {
		t.Errorf("long root off Windows: %v", err)
	}
}

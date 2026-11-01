package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/nvim"
)

// o opens files in the editor used last; with none, in the neovim xf runs
// beside; with neither, in the editor setting's; with none of them, it
// says there is no editor.
func TestEditorOpener(t *testing.T) {
	var inNvim []string
	nvimOpen := func(file string, line int) error { inNvim = append(inNvim, file); return nil }
	showing := func(err error) func(context.Context, string, int) error {
		return func(context.Context, string, int) error { return err }
	}
	boom := errors.New("the editor didn't open it")

	if _, err := editorOpener(showing(nil), nvimOpen, "code", "/p")("/p/a.sqlx", 3); err != nil || len(inNvim) != 0 {
		t.Errorf("with an editor: %v, nvim asked %v", err, inNvim)
	}
	if _, err := editorOpener(showing(boom), nvimOpen, "code", "/p")("/p/a.sqlx", 3); !errors.Is(err, boom) || len(inNvim) != 0 {
		t.Errorf("when the editor fails: %v, nvim asked %v", err, inNvim)
	}
	if _, err := editorOpener(showing(remote.ErrNoEditor), nvimOpen, "code", "/p")("/p/a.sqlx", 3); err != nil || len(inNvim) != 1 {
		t.Errorf("with no editor, beside nvim: %v, nvim asked %v", err, inNvim)
	}
	_, err := editorOpener(showing(remote.ErrNoEditor), nil, "", "/p")("/p/a.sqlx", 3)
	if !errors.Is(err, remote.ErrNoEditor) || err.Error() != "no editor connected" {
		t.Errorf("with no editor at all: %v", err)
	}
	_, err = editorOpener(showing(remote.ErrNoEditor), nil, "xf-no-such-editor", "/p")("/p/a.sqlx", 3)
	if err == nil || !strings.Contains(err.Error(), "xf-no-such-editor isn't on PATH") {
		t.Errorf("with an editor setting that isn't installed: %v", err)
	}
}

// fakeEditors puts empty programs with these names on PATH.
func fakeEditors(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if runtime.GOOS == "windows" {
			n += ".bat"
		}
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// A terminal editor is left for xf to run, in the project; neovim replaces
// xf, where it can.
func TestOpenWith(t *testing.T) {
	fakeEditors(t, "vim", "nvim")
	root := t.TempDir()
	t.Setenv(nvim.ServerEnv, "")
	// Windows comes last; before it, as anywhere neovim can replace xf.
	goos = "linux"
	defer func() { goos = runtime.GOOS }()

	o, err := openWith("vim", root, "", 0)
	if err != nil || o.Run == nil || o.Leave || o.Run.Dir != root || !slices.Equal(o.Run.Args[1:], []string{root}) {
		t.Errorf("vim on a project: %+v, %v", o, err)
	}
	if o, err := openWith("nvim", root, filepath.Join(root, "a.sqlx"), 3); err != nil || !o.Leave || o.Run != nil {
		t.Errorf("nvim: %+v, %v", o, err)
	}
	t.Setenv(nvim.ServerEnv, "/tmp/nvim.sock")
	if o, err := openWith("nvim", root, "", 0); !errors.Is(err, errNvimNested) || o.Leave {
		t.Errorf("nvim inside neovim: %+v, %v", o, err)
	}
	t.Setenv(nvim.ServerEnv, "")
	goos = "windows"
	if o, err := openWith("nvim", root, "", 0); !errors.Is(err, errNvimWindows) || o.Leave {
		t.Errorf("nvim on Windows: %+v, %v", o, err)
	}
}

// Without the editor setting, $VISUAL wins over $EDITOR, and VS Code is
// the default.
func TestDefaultEditor(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if e, _ := defaultEditor(getenv); e != "code" {
		t.Errorf("with neither: %q", e)
	}
	env["EDITOR"] = "vim"
	if e, src := defaultEditor(getenv); e != "vim" || src != "$EDITOR" {
		t.Errorf("with $EDITOR: %q from %s", e, src)
	}
	env["VISUAL"] = " nvim "
	if e, src := defaultEditor(getenv); e != "nvim" || src != "$VISUAL" {
		t.Errorf("with both: %q from %s", e, src)
	}
}

// The editor setting's command gets the project's folder and, for editors
// xf knows, the file at its 1-based line; without a file, the folder alone.
func TestEditorArgs(t *testing.T) {
	for _, c := range []struct {
		editor, file string
		want         []string
		kind         editorKind
	}{
		{"code", "/p/a.sqlx", []string{"code", "/p", "--goto", "/p/a.sqlx:4"}, editorWindow},
		{"code --reuse-window", "/p/a.sqlx", []string{"code", "--reuse-window", "/p", "--goto", "/p/a.sqlx:4"}, editorWindow},
		{"/usr/local/bin/cursor", "/p/a.sqlx", []string{"/usr/local/bin/cursor", "/p", "--goto", "/p/a.sqlx:4"}, editorWindow},
		{"zed", "/p/a.sqlx", []string{"zed", "/p", "/p/a.sqlx:4"}, editorWindow},
		{"nvim", "/p/a.sqlx", []string{"nvim", "+4", "/p/a.sqlx"}, editorNvim},
		{"vim", "/p/a.sqlx", []string{"vim", "+4", "/p/a.sqlx"}, editorTerminal},
		{"hx", "/p/a.sqlx", []string{"hx", "/p/a.sqlx:4"}, editorTerminal},
		{"idea", "/p/a.sqlx", []string{"idea", "/p/a.sqlx"}, editorWindow},
		{"code", "", []string{"code", "/p"}, editorWindow},
		{"idea", "", []string{"idea", "/p"}, editorWindow},
		{"nvim", "", []string{"nvim", "/p"}, editorNvim},
		{"vim", "", []string{"vim", "/p"}, editorTerminal},
	} {
		got, kind := editorArgs(c.editor, "/p", c.file, 3)
		if !slices.Equal(got, c.want) || kind != c.kind {
			t.Errorf("editorArgs(%q, %q) = %q, kind %v; want %q, %v", c.editor, c.file, got, kind, c.want, c.kind)
		}
	}
}

// xf web offers only editors with a window of their own, on PATH.
func TestBrowserEditorErr(t *testing.T) {
	for _, editor := range []string{"nvim", "vim -u NONE", "hx", ""} {
		if browserEditorErr(editor) == nil {
			t.Errorf("%q: no error, want refused", editor)
		}
	}
	if err := browserEditorErr("xf-no-such-editor"); err == nil || !strings.Contains(err.Error(), "isn't on PATH") {
		t.Errorf("missing editor: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := browserEditorErr("true --flag"); err != nil {
			t.Errorf("a window editor on PATH: %v", err)
		}
	}
}

package project

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// EditorEvent says which file is active in the editor. The nvim plugin in
// nvim/plugin/xf.lua sends one per BufEnter, FocusGained and CursorHold,
// through its xf language server.
type EditorEvent struct {
	// Event is the autocmd that fired, e.g. "BufEnter" or "CursorHold".
	Event string
	// File is the active file relative to the project root, with forward
	// slashes, as in a compiled action's fileName.
	File string
}

// EditorEventFor makes the event for the editor's absolute path file in the
// project at root. An empty event is a BufEnter. It fails for a file outside
// root or a relative path.
func EditorEventFor(root, event, file string) (EditorEvent, bool) {
	file = strings.TrimSpace(file)
	if event == "" {
		event = "BufEnter"
	}
	if file == "" || !filepath.IsAbs(file) {
		return EditorEvent{}, false
	}
	rel, err := filepath.Rel(RealPath(root), RealPath(file))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return EditorEvent{}, false
	}
	return EditorEvent{Event: event, File: filepath.ToSlash(rel)}, true
}

// RealPath resolves symlinks in p, or returns it as it is when it can't.
func RealPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return CanonicalDrive(r)
	}
	return CanonicalDrive(p)
}

// CanonicalDrive upper-cases a Windows path's drive letter (c:\x is C:\x),
// so a path names one engine, session and cache whichever way it was
// typed. Other paths are returned as they are.
func CanonicalDrive(p string) string {
	if len(p) >= 2 && p[1] == ':' && 'a' <= p[0] && p[0] <= 'z' {
		return string(p[0]-'a'+'A') + p[1:]
	}
	return p
}

// MaxRootLen is the longest project root path xf runs tools in on Windows:
// dbt-core can't read a project deeper than that, Fusion panics, and Go
// can't start a process in a working directory of 259 characters or more.
const MaxRootLen = 258

// goos is the OS CheckRoot judges for; tests change it.
var goos = runtime.GOOS

// CheckRoot fails, saying what to do, for a project root the tools can't
// work in: on Windows, one longer than MaxRootLen.
func CheckRoot(root string) error {
	if goos == "windows" && len(root) > MaxRootLen {
		return fmt.Errorf("the project's path is %d characters long, and on Windows dbt and the tools xf runs can't work in a project whose path is longer than %d: move or clone it somewhere shorter, such as C:\\src\\%s", len(root), MaxRootLen, filepath.Base(root))
	}
	return nil
}

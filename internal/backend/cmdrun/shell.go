package cmdrun

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ShellJoin renders args as a command line to copy into a terminal: POSIX
// shell quoting, or on Windows quoting that pastes into PowerShell and cmd
// alike.
func ShellJoin(args []string) string { return shellJoin(runtime.GOOS, args) }

func shellJoin(goos string, args []string) string {
	quote := posixQuote
	if goos == "windows" {
		quote = windowsQuote
	}
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = quote(a)
	}
	return strings.Join(out, " ")
}

// posixQuote single-quotes an argument a POSIX shell would split or expand.
func posixQuote(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"'$`\\*?[]{}()<>|&;#~") {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// windowsQuote double-quotes an argument PowerShell or cmd would split or
// treat specially; both read "..." the same. Inside double quotes
// PowerShell still expands $ and `, and the two disagree on escaping a ",
// so an argument with those is single-quoted instead, as PowerShell (the
// default in Windows Terminal and VS Code) reads it.
func windowsQuote(a string) string {
	if strings.ContainsAny(a, "$`\"") {
		return "'" + strings.ReplaceAll(a, "'", "''") + "'"
	}
	if a != "" && !strings.ContainsAny(a, " \t&|<>^()%!;,=@'{}[]#") {
		return a
	}
	return `"` + a + `"`
}

// CommandName is how a command line names bin, the tool known on PATH as
// name, so that pasting the line runs the same binary: name when looking it
// up on PATH finds bin, else bin's path, relative to root (where the
// command runs) when it's inside it.
func CommandName(bin, name, root string) string {
	if p, err := exec.LookPath(name); err == nil && sameFile(p, bin) {
		return name
	}
	if root != "" {
		if rel, err := filepath.Rel(root, bin); err == nil && filepath.IsLocal(rel) {
			if !strings.ContainsRune(rel, filepath.Separator) {
				rel = "." + string(filepath.Separator) + rel // not a name to look up
			}
			return rel
		}
	}
	return bin
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

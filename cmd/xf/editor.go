package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/internal/web"
	"github.com/ashish10alex/xf/nvim"
)

// editorOpener opens files for the TUI's o key in the editor used last,
// with show (remote.Client.ShowDocument). When no editor has said which
// file is active, it opens them with inNvim, the neovim xf runs beside, if
// there is one, and else with editor, the editor setting's command, in the
// project at root.
func editorOpener(show func(ctx context.Context, file string, line int) error, inNvim func(file string, line int) error, editor, root string) func(file string, line int) (frontend.Opening, error) {
	return func(file string, line int) (frontend.Opening, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := show(ctx, file, line)
		if !errors.Is(err, remote.ErrNoEditor) {
			return frontend.Opening{}, err
		}
		if inNvim != nil {
			return frontend.Opening{}, inNvim(file, line)
		}
		if strings.TrimSpace(editor) == "" {
			return frontend.Opening{}, err
		}
		return openWith(editor, root, file, line)
	}
}

// Why neovim with xf beside it can't be opened.
var (
	errNvimWindows = errors.New("neovim with xf beside it isn't supported on Windows")
	errNvimNested  = errors.New("already inside neovim: press enter to open it here")
)

// goos is the OS; tests change it.
var goos = runtime.GOOS

// openWith opens the project at root with editor, the editor setting's
// command, and in it file (an absolute path) at a 0-based line; file ""
// opens the project alone. A window editor has it when openWith returns.
// A terminal editor is left for the caller to run, and neovim for the
// caller to replace xf with (see frontend.Opening).
func openWith(editor, root, file string, line int) (frontend.Opening, error) {
	args, kind := editorArgs(editor, root, file, line)
	prog, err := exec.LookPath(args[0])
	if err != nil {
		return frontend.Opening{}, fmt.Errorf("%s isn't on PATH (set %s in xf's config, e.g. nvim)", args[0], config.KeyEditor)
	}
	if kind == editorNvim {
		switch {
		case goos == "windows":
			return frontend.Opening{}, errNvimWindows
		case os.Getenv(nvim.ServerEnv) != "":
			return frontend.Opening{}, errNvimNested
		}
		return frontend.Opening{Leave: true}, nil
	}
	cmd := exec.Command(prog, args[1:]...)
	cmd.Dir = root
	if kind == editorTerminal {
		return frontend.Opening{Run: cmd}, nil
	}
	out, err := cmd.CombinedOutput()
	if err != nil && len(bytes.TrimSpace(out)) > 0 {
		return frontend.Opening{}, errors.New(string(bytes.TrimSpace(out)))
	}
	return frontend.Opening{}, err
}

// defaultEditor is the editor command when the editor setting isn't set,
// and where it came from: $VISUAL, then $EDITOR, then VS Code.
func defaultEditor(getenv func(string) string) (editor, source string) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(getenv(env)); v != "" {
			return v, "$" + env
		}
	}
	return config.DefaultEditor, "default: o opens files with it when no editor is connected"
}

// first is the first of two values.
func first[A, B any](a A, _ B) A { return a }

// editorKind is how an editor is run.
type editorKind int

const (
	// editorWindow has a window of its own; its command returns at once.
	editorWindow editorKind = iota
	// editorTerminal draws in the terminal it is started from.
	editorTerminal
	// editorNvim is neovim, which xf runs beside (xf --nvim).
	editorNvim
)

// editorArgs is the command that opens the project at root with editor,
// the editor setting: a program and any arguments of its own, e.g. "code"
// or "code --reuse-window". With a file, it opens that at a 0-based line,
// in the project's folder for the window editors xf knows, so the file
// lands in the project's window rather than the one used last. An editor
// xf doesn't know gets the file alone, or the folder when there is no file.
func editorArgs(editor, root, file string, line int) (args []string, kind editorKind) {
	args = strings.Fields(editor)
	name := strings.ToLower(filepath.Base(args[0]))
	name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".exe"), ".cmd"), ".bat")
	switch name {
	case "nvim":
		kind = editorNvim
	case "vim", "vi", "hx", "nano", "micro":
		kind = editorTerminal
	}
	if file == "" {
		return append(args, root), kind
	}
	at := file + ":" + strconv.Itoa(line+1)
	switch name {
	case "code", "code-insiders", "codium", "cursor", "windsurf":
		return append(args, root, "--goto", at), kind
	case "zed", "subl":
		return append(args, root, at), kind
	case "hx":
		return append(args, at), kind
	case "nvim", "vim", "vi", "nano", "micro":
		return append(args, "+"+strconv.Itoa(line+1), file), kind
	}
	return append(args, file), kind
}

// windowEditors are the window editors xf web offers when o can't run the
// editor it has, in the order offered.
var windowEditors = []web.EditorChoice{
	{Command: "code", Name: "VS Code"},
	{Command: "cursor", Name: "Cursor"},
	{Command: "windsurf", Name: "Windsurf"},
	{Command: "zed", Name: "Zed"},
	{Command: "subl", Name: "Sublime Text"},
	{Command: "code-insiders", Name: "VS Code Insiders"},
	{Command: "codium", Name: "VSCodium"},
}

// browserEditorErr says why o can't open files with editor from xf web: a
// terminal editor (neovim among them) needs the terminal, and any editor
// needs to be on PATH.
func browserEditorErr(editor string) error {
	if strings.TrimSpace(editor) == "" {
		return errors.New("no editor command given")
	}
	args, kind := editorArgs(editor, "", "", 0)
	if kind != editorWindow {
		return fmt.Errorf("%s runs in a terminal, which xf web can't open: pick an editor with a window of its own", args[0])
	}
	if _, err := exec.LookPath(args[0]); err != nil {
		return fmt.Errorf("%s isn't on PATH", args[0])
	}
	return nil
}

// webEditor is the editor command o uses in xf web, which the page can
// change.
type webEditor struct {
	root string
	// load reads the settings again, for Reload; nil can't.
	load     func() (config.Loaded, error)
	mu       sync.Mutex
	command  string
	from     string
	lookPath func(string) (string, error)
}

func newWebEditor(root string, settings config.Loaded) *webEditor {
	e := &webEditor{root: root, lookPath: exec.LookPath}
	e.take(settings)
	return e
}

// take uses the editor settings name, else the default; e.mu is held or
// not yet shared.
func (e *webEditor) take(settings config.Loaded) {
	if settings.IsSet(config.KeyEditor) && settings.Editor != "" {
		e.command, e.from = settings.Editor, settings.Sources[config.KeyEditor]
	} else {
		e.command, e.from = defaultEditor(os.Getenv)
	}
}

func (e *webEditor) get() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.command
}

// hook is what the page needs of e (see web.Editor).
func (e *webEditor) hook() *web.Editor {
	return &web.Editor{
		Current: func() (string, string, bool) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.command, e.from, browserEditorErr(e.command) == nil
		},
		Choices: func() []web.EditorChoice {
			out := slices.Clone(windowEditors)
			for i := range out {
				_, err := e.lookPath(out[i].Command)
				out[i].Found = err == nil
			}
			return out
		},
		Set: func(command string, project bool) (string, error) {
			if err := browserEditorErr(command); err != nil {
				return "", err
			}
			var file string
			var err error
			if project {
				file, err = config.SetProjectKey(e.root, config.KeyEditor, command)
			} else {
				file, err = config.SetUserKey(config.KeyEditor, command)
			}
			if err != nil {
				return file, err
			}
			e.mu.Lock()
			e.command, e.from = command, file
			e.mu.Unlock()
			return file, nil
		},
		Reload: func() {
			if e.load == nil {
				return
			}
			if l, err := e.load(); err == nil {
				e.mu.Lock()
				e.take(l)
				e.mu.Unlock()
			}
		},
	}
}

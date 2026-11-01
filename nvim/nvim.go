// Package nvim holds xf's neovim runtime (plugin/, lsp/ and lua/, embedded
// so `xf --nvim` works without installing the plugin) and the two ways xf
// talks to neovim: starting it with xf in a split, and asking the neovim
// xf runs inside ($NVIM) to open a file.
package nvim

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed plugin lsp lua site
var runtime embed.FS

// ServerEnv is the variable neovim sets in its terminals to its own RPC
// address.
const ServerEnv = "NVIM"

// ErrNested is returned by Launch inside a neovim terminal, where a second
// neovim would run inside the first.
var ErrNested = errors.New("already inside nvim: run :Xf to open xf in a split")

// Install writes the embedded runtime into a directory under cache named
// for its contents, unless an earlier xf already did, and returns it.
func Install(cache string) (string, error) {
	sum := sha256.New()
	err := fs.WalkDir(runtime, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := runtime.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(sum, "%s\x00%d\x00", path, len(b))
		_, _ = sum.Write(b)
		return nil
	})
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "nvim-"+hex.EncodeToString(sum.Sum(nil))[:12])
	if _, err := os.Stat(filepath.Join(dir, "lua", "xf.lua")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cache, "nvim-tmp-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	err = fs.WalkDir(runtime, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := runtime.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(filepath.Join(dir, "lua", "xf.lua")); statErr == nil {
			return dir, nil // another xf installed it first
		}
		return "", err
	}
	return dir, nil
}

// LaunchOptions says how to start neovim with xf beside it.
type LaunchOptions struct {
	// Runtime is the directory Install returned.
	Runtime string
	// Xf is the command the split runs: this xf and its flags.
	Xf []string
	// LSP is the language server command, used when the user's config
	// doesn't enable xf's language server itself.
	LSP []string
	// Files are opened in the editor; none opens the working directory, as
	// `nvim .` does, so a file explorer that takes over directories shows.
	Files []string
	// Line, when positive, is the 1-based line of the first file to put
	// the cursor on.
	Line int
}

// Args returns neovim's arguments (after the program name): the files,
// then a command that, once neovim has started, loads the plugin from the
// runtime if the user's config didn't, and opens :Xf. It waits for
// VimEnter so that enabling the language server attaches it to the files.
func Args(o LaunchOptions) ([]string, error) {
	opts, err := json.Marshal(map[string]any{"runtime": o.Runtime, "cmd": o.Xf, "lsp_cmd": o.LSP})
	if err != nil {
		return nil, err
	}
	const lq, rq = "[=====[", "]=====]"
	if strings.Contains(string(opts), rq) {
		return nil, fmt.Errorf("can't pass %s to neovim", opts)
	}
	lua := "lua vim.api.nvim_create_autocmd('VimEnter', { once = true, nested = true, callback = function()" +
		" local o = vim.json.decode(" + lq + string(opts) + rq + ")" +
		" dofile(o.runtime .. '/lua/xf.lua').launch(o) end })"
	files := o.Files
	if len(files) == 0 {
		files = []string{"."}
	}
	args := []string{"-c", lua}
	if o.Line > 0 && len(o.Files) > 0 {
		args = append(args, "+"+strconv.Itoa(o.Line))
	}
	return append(append(args, "--"), files...), nil
}

// EditArgs returns the arguments that make the neovim at server open file
// at a 0-based line in its editor window (lua/xf.lua's edit).
func EditArgs(server, file string, line int) []string {
	expr := fmt.Sprintf(`luaeval("require'xf'.edit(_A[1], _A[2])", ['%s', %d])`, strings.ReplaceAll(file, "'", "''"), line)
	return []string{"--server", server, "--remote-expr", expr}
}

// FullScreenArgs returns the arguments that make the neovim at server show
// the xf that is its current window full screen, or put it back in its
// split (lua/xf.lua's toggle_full_screen).
func FullScreenArgs(server string) []string {
	return []string{"--server", server, "--remote-expr", `luaeval("require'xf'.toggle_full_screen()")`}
}

// TermEnv is what a shell in xf web's terminal tab gets added to its
// environment, so a neovim started there loads xf's plugin and language
// server from rt, the directory Install returned (site/…/xf_terminal.lua).
// xf is the command :Xf runs and lsp the language server's.
func TermEnv(rt string, xf, lsp []string) ([]string, error) {
	opts, err := json.Marshal(map[string]any{"runtime": rt, "cmd": xf, "lsp_cmd": lsp})
	if err != nil {
		return nil, err
	}
	// Set, XDG_DATA_DIRS replaces neovim's default rather than adding to it.
	dirs := os.Getenv("XDG_DATA_DIRS")
	if dirs == "" {
		dirs = "/usr/local/share" + string(os.PathListSeparator) + "/usr/share"
	}
	return []string{
		"XDG_DATA_DIRS=" + filepath.Join(rt, "site") + string(os.PathListSeparator) + dirs,
		"XF_NVIM=" + string(opts),
	}, nil
}

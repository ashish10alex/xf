package nvim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/testutil"
)

func nvimBin(t *testing.T) string {
	t.Helper()
	nvim, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim not on PATH")
	}
	return nvim
}

func projectFile(t *testing.T) string {
	t.Helper()
	f, err := filepath.Abs(filepath.Join("..", "testdata", "projects", "navigation", "definitions", "marts", "report.sqlx"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// runLua runs lua in headless neovim, in dir with args after it, and
// decodes the JSON the script passes to report.
// standIns puts stand-ins for xf in $XF_TEST_WAIT (waits for input, as
// the TUI does) and $XF_TEST_FAIL (exits at once), for the Lua and Vim
// script the tests run, and returns them.
func standIns(t *testing.T) (wait, fail string) {
	t.Helper()
	wait, fail = testutil.FakeCLI(t, "xf-wait"), testutil.FakeCLI(t, "xf-fail")
	t.Setenv("XF_TEST_WAIT", wait)
	t.Setenv("XF_TEST_FAIL", fail)
	return wait, fail
}

func runLua(t *testing.T, dir string, args []string, lua string, into any) {
	t.Helper()
	standIns(t)
	out := filepath.Join(t.TempDir(), "out.json")
	script := filepath.Join(t.TempDir(), "run.lua")
	lua = fmt.Sprintf("local out = %q\nlocal function report(v) local f = assert(io.open(out, 'w')); f:write(vim.json.encode(v)); f:close() end\n", out) + lua
	if err := os.WriteFile(script, []byte(lua), 0o644); err != nil {
		t.Fatal(err)
	}
	// The script's -c goes first: args may end with -- and files.
	all := append([]string{"--headless", "-i", "NONE", "-n", "-c", "luafile " + script}, args...)
	// A script that fails leaves neovim waiting, so give up after a while.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nvimBin(t), all...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+t.TempDir(), "XDG_DATA_HOME="+t.TempDir(), "NVIM=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nvim: %v\n%s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the script reported nothing: %v", err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
}

// TestXfSplit opens :Xf with a stand-in for xf and checks the layout,
// that a second :Xf focuses it, that edit opens files in the editor
// window, and that the split closes when xf exits.
func TestXfSplit(t *testing.T) {
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	file := projectFile(t)
	var got struct {
		Wins, WinsAgain, WinsAfterExit int
		Width, Columns                 int
		StartedInXf, Refocused         bool
		EditFile                       string
		EditInEditor                   bool
		CtrlW                          bool
	}
	runLua(t, "", []string{"-u", "NONE"}, fmt.Sprintf(`
vim.opt.rtp:append(%q)
vim.cmd("runtime plugin/xf.lua")
vim.o.columns = 100
vim.g.xf_cmd = { os.getenv("XF_TEST_WAIT") }
local editor = vim.api.nvim_get_current_win()
vim.cmd("Xf")
local r = { Columns = vim.o.columns }
r.Wins = #vim.api.nvim_tabpage_list_wins(0)
local xf
for _, w in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
  if w ~= editor then xf = w end
end
r.StartedInXf = vim.api.nvim_get_current_win() == xf
r.Width = vim.api.nvim_win_get_width(xf)
local buf = vim.api.nvim_win_get_buf(xf)
r.CtrlW = vim.fn.maparg("<C-w>", "t", false, true).buffer == 1
vim.api.nvim_set_current_win(editor)
vim.cmd("Xf")
r.Refocused = vim.api.nvim_get_current_win() == xf
vim.api.nvim_set_current_win(editor)
vim.api.nvim_set_current_win(xf)
require("xf").edit(%q)
r.EditFile = vim.api.nvim_buf_get_name(0)
r.EditInEditor = vim.api.nvim_get_current_win() == editor
vim.cmd("Xf")
r.WinsAgain = #vim.api.nvim_tabpage_list_wins(0)
vim.fn.chansend(vim.bo[buf].channel, "\n")
vim.wait(15000, function() return #vim.api.nvim_tabpage_list_wins(0) == 1 end)
r.WinsAfterExit = #vim.api.nvim_tabpage_list_wins(0)
report(r)
vim.cmd("qa!")
`, dir, file), &got)

	if got.Wins != 2 || got.WinsAgain != 2 {
		t.Errorf("windows = %d, then %d after :Xf again; want 2", got.Wins, got.WinsAgain)
	}
	if want := got.Columns / 2; got.Width < want-1 || got.Width > want+1 {
		t.Errorf("xf width = %d of %d columns, want about %d", got.Width, got.Columns, want)
	}
	if !got.StartedInXf {
		t.Error(":Xf didn't focus xf")
	}
	if !got.CtrlW {
		t.Error("no <C-w> terminal mapping in the xf buffer")
	}
	if !got.Refocused {
		t.Error(":Xf with xf open didn't focus it")
	}
	if got.EditFile != file || !got.EditInEditor {
		t.Errorf("edit opened %q (in the editor window: %v), want %q there", got.EditFile, got.EditInEditor, file)
	}
	if got.WinsAfterExit != 1 {
		t.Errorf("windows after xf exits = %d, want 1", got.WinsAfterExit)
	}
}

// TestFullScreen toggles xf full screen and back, checks that leaving it
// for the editor puts it back too, and that o opens files in the editor
// from full screen.
func TestFullScreen(t *testing.T) {
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	file := projectFile(t)
	type wins struct {
		Wins, Floats, Width, Height int
		InXf                        bool
	}
	var got struct {
		Full, Back, Left, Edited wins
		Columns, Lines           int
		EditFile                 string
	}
	runLua(t, "", []string{"-u", "NONE"}, fmt.Sprintf(`
vim.opt.rtp:append(%q)
vim.cmd("runtime plugin/xf.lua")
vim.o.columns = 100
vim.o.lines = 30
vim.g.xf_cmd = { os.getenv("XF_TEST_WAIT") }
local editor = vim.api.nvim_get_current_win()
vim.cmd("Xf")
local function wins()
  local w = { Wins = #vim.api.nvim_tabpage_list_wins(0), Floats = 0 }
  for _, win in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
    if vim.api.nvim_win_get_config(win).relative ~= "" then w.Floats = w.Floats + 1 end
  end
  w.Width = vim.api.nvim_win_get_width(0)
  w.Height = vim.api.nvim_win_get_height(0)
  w.InXf = vim.bo.buftype == "terminal"
  return w
end
local xf = require("xf")
local r = { Columns = vim.o.columns, Lines = vim.o.lines }
xf.toggle_full_screen()
r.Full = wins()
xf.toggle_full_screen()
r.Back = wins()
xf.toggle_full_screen()
vim.api.nvim_set_current_win(editor)
vim.wait(1000, function() return #vim.api.nvim_tabpage_list_wins(0) == 2 end)
r.Left = wins()
vim.cmd("Xf")
xf.toggle_full_screen()
xf.edit(%q)
r.Edited = wins()
r.EditFile = vim.api.nvim_buf_get_name(0)
report(r)
vim.cmd("qa!")
`, dir, file), &got)

	if f := got.Full; f.Wins != 3 || f.Floats != 1 || !f.InXf || f.Width != got.Columns || f.Height < got.Lines-2 {
		t.Errorf("full screen: %+v, want xf focused in a %dx%d float over 2 windows", f, got.Columns, got.Lines-1)
	}
	if b := got.Back; b.Wins != 2 || b.Floats != 0 || !b.InXf || b.Width != 50 {
		t.Errorf("back: %+v, want xf focused in its 50-column split", b)
	}
	if l := got.Left; l.Wins != 2 || l.Floats != 0 || l.InXf {
		t.Errorf("after leaving for the editor: %+v, want the float closed, the editor focused", l)
	}
	if e := got.Edited; e.Wins != 2 || e.Floats != 0 || e.InXf || got.EditFile != file {
		t.Errorf("o from full screen: %+v in %q, want %q in the editor, the float closed", e, got.EditFile, file)
	}
}

// TestBufferCycling checks that :bnext and :bprevious in the editor skip
// xf, and that xf's split and its full-screen float refuse another
// buffer, leaving xf running in its one window.
func TestBufferCycling(t *testing.T) {
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	file := projectFile(t)
	other := filepath.Join(filepath.Dir(file), "other.sqlx")
	var got struct {
		Listed                        bool
		Cycled                        []string
		XfWins                        int
		SplitErr, FloatErr, NoWinErr  string
		SplitKept, FloatKept, Running bool
		NoWinFile                     string
		NoWinWins                     int
	}
	runLua(t, "", []string{"-u", "NONE"}, fmt.Sprintf(`
vim.opt.rtp:append(%q)
vim.cmd("runtime plugin/xf.lua")
vim.o.columns = 100
vim.o.hidden = true
vim.g.xf_cmd = { os.getenv("XF_TEST_WAIT") }
local editor = vim.api.nvim_get_current_win()
vim.cmd.edit(%q)
vim.cmd.edit(%q)
vim.cmd("Xf")
local xfwin = vim.api.nvim_get_current_win()
local buf = vim.api.nvim_get_current_buf()
local r = { Listed = vim.bo[buf].buflisted, Cycled = {} }
vim.api.nvim_set_current_win(editor)
for _, c in ipairs({ "bnext", "bnext", "bnext", "bprevious", "bprevious", "bprevious" }) do
  vim.cmd(c)
  table.insert(r.Cycled, vim.bo.buftype)
end
r.XfWins = #vim.fn.win_findbuf(buf)
local function try(c)
  local ok, err = pcall(vim.cmd, c)
  return ok and "" or tostring(err)
end
vim.api.nvim_set_current_win(xfwin)
r.SplitErr = try("bnext")
r.SplitKept = vim.api.nvim_win_get_buf(xfwin) == buf
local xf = require("xf")
xf.toggle_full_screen()
r.FloatErr = try("bnext")
r.FloatKept = vim.api.nvim_get_current_buf() == buf
xf.toggle_full_screen()
r.Running = vim.fn.jobwait({ vim.bo[buf].channel }, 0)[1] == -1
-- with no window but xf's, o still makes one for the file
vim.api.nvim_win_close(editor, true)
xf.edit(%q)
r.NoWinFile = vim.api.nvim_buf_get_name(0)
r.NoWinWins = #vim.api.nvim_tabpage_list_wins(0)
r.NoWinErr = try("bnext")
report(r)
vim.cmd("qa!")
`, dir, file, other, file), &got)

	if got.Listed {
		t.Error("the xf buffer is listed")
	}
	if len(got.Cycled) != 6 || slices.Contains(got.Cycled, "terminal") {
		t.Errorf("buftypes cycling in the editor = %q, want 6 files, never xf", got.Cycled)
	}
	if got.XfWins != 1 {
		t.Errorf("xf is in %d windows after cycling, want 1", got.XfWins)
	}
	if !strings.Contains(got.SplitErr, "E1513") || !got.SplitKept {
		t.Errorf(":bnext in the xf split: error %q, xf kept: %v; want E1513 and xf kept", got.SplitErr, got.SplitKept)
	}
	if !strings.Contains(got.FloatErr, "E1513") || !got.FloatKept {
		t.Errorf(":bnext in the full-screen float: error %q, xf kept: %v; want E1513 and xf kept", got.FloatErr, got.FloatKept)
	}
	if !got.Running {
		t.Error("xf stopped")
	}
	if got.NoWinFile != file || got.NoWinWins != 2 || got.NoWinErr != "" {
		t.Errorf("o with only xf's window: %q in %d windows, :bnext there: %q; want %q in 2, no error", got.NoWinFile, got.NoWinWins, got.NoWinErr, file)
	}
}

func TestFullScreenArgs(t *testing.T) {
	got := FullScreenArgs("/tmp/s")
	want := []string{"--server", "/tmp/s", "--remote-expr", `luaeval("require'xf'.toggle_full_screen()")`}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestEditReplacesExplorer opens a file from xf when the only other
// window is a file explorer's, as when nvim-tree takes over the directory
// xf --nvim opened: the file takes the explorer's place and xf keeps its
// width.
func TestEditReplacesExplorer(t *testing.T) {
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	file := projectFile(t)
	var got struct {
		Names  []string
		Width  int
		Editor bool
	}
	runLua(t, "", []string{"-u", "NONE"}, fmt.Sprintf(`
vim.opt.rtp:append(%q)
vim.cmd("runtime plugin/xf.lua")
vim.o.columns = 100
vim.bo.buftype = "nofile" -- the explorer
vim.api.nvim_buf_set_name(0, "Explorer")
vim.wo.winfixbuf = true -- explorers keep other buffers out of their window
vim.g.xf_cmd = { os.getenv("XF_TEST_WAIT") }
vim.cmd("Xf")
require("xf").edit(%q)
local r = { Names = {} }
for _, w in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
  local b = vim.api.nvim_win_get_buf(w)
  table.insert(r.Names, vim.bo[b].buftype == "terminal" and "xf" or vim.fn.fnamemodify(vim.api.nvim_buf_get_name(b), ":t"))
  if vim.bo[b].buftype == "terminal" then r.Width = vim.api.nvim_win_get_width(w) end
end
r.Editor = vim.bo.buftype == ""
report(r)
vim.cmd("qa!")
`, dir, file), &got)
	if !slices.Equal(got.Names, []string{"report.sqlx", "xf"}) || got.Width != 50 || !got.Editor {
		t.Errorf("windows %q, xf width %d, editor focused %v; want [report.sqlx xf], 50, true", got.Names, got.Width, got.Editor)
	}
}

// TestLaunchBesideExplorer launches xf when a file explorer has taken
// over the only window, as nvim-tree does with the directory xf --nvim
// opens: an empty window is made beside it for the explorer to open files
// in, since it won't use xf's, and the explorer keeps the focus.
func TestLaunchBesideExplorer(t *testing.T) {
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Names        []string
		Width, Empty int
		Focused      string
	}
	runLua(t, "", []string{"-u", "NONE"}, fmt.Sprintf(`
vim.o.columns = 100
vim.bo.buftype = "nofile"
vim.api.nvim_buf_set_name(0, "Explorer")
vim.wo.winfixbuf = true
vim.wo.winfixwidth = true -- as nvim-tree's window is
dofile(%q .. "/lua/xf.lua").launch({ runtime = %q, cmd = { os.getenv("XF_TEST_WAIT") }, lsp_cmd = { os.getenv("XF_TEST_FAIL") } })
local r = { Names = {} }
for _, w in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
  local b = vim.api.nvim_win_get_buf(w)
  table.insert(r.Names, vim.bo[b].buftype == "terminal" and "xf" or vim.fn.fnamemodify(vim.api.nvim_buf_get_name(b), ":t"))
  if vim.bo[b].buftype == "terminal" then r.Width = vim.api.nvim_win_get_width(w) end
  if vim.api.nvim_buf_get_name(b) == "" then r.Empty = vim.api.nvim_win_get_width(w) end
end
r.Focused = vim.fn.fnamemodify(vim.api.nvim_buf_get_name(0), ":t")
report(r)
vim.cmd("qa!")
`, dir, dir), &got)
	if got.Empty < 20 {
		t.Errorf("the empty window is %d columns wide, want about half the explorer's", got.Empty)
	}
	if !slices.Equal(got.Names, []string{"Explorer", "", "xf"}) || got.Width != 50 || got.Focused != "Explorer" {
		t.Errorf("windows %q, xf width %d, focus on %q; want [Explorer  xf], 50, Explorer", got.Names, got.Width, got.Focused)
	}
}

// TestLaunch starts neovim as xf --nvim does, without the plugin in the
// user's config, and checks that it loads the plugin and language server
// from the installed runtime and opens xf beside the files, or beside the
// working directory when there are none, leaving the focus in the editor.
func TestLaunch(t *testing.T) {
	rt, err := Install(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := projectFile(t)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	wait, fail := standIns(t)
	for _, c := range []struct {
		files      []string
		wantEditor string
	}{
		{[]string{file}, file},
		{nil, root},
	} {
		args, err := Args(LaunchOptions{
			Runtime: rt,
			Xf:      []string{wait},
			LSP:     []string{fail, "lsp"},
			Files:   c.files,
		})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Cmd     []string
			LSPCmd  []string
			Enabled bool
			Wins    int
			Editor  string
			Focused string
		}
		// Scheduled from VimEnter, so it runs once the launcher has opened xf.
		lua := `
vim.api.nvim_create_autocmd("VimEnter", { once = true, callback = function()
  vim.schedule(function()
    report({
      Cmd = vim.g.xf_cmd,
      LSPCmd = vim.lsp.config.xf and vim.lsp.config.xf.cmd or {},
      Enabled = vim.lsp.is_enabled("xf"),
      Wins = #vim.api.nvim_tabpage_list_wins(0),
      -- netrw leaves the directory's buffer unnamed
      Editor = vim.b[vim.fn.winbufnr(1)].netrw_curdir or vim.api.nvim_buf_get_name(vim.fn.winbufnr(1)),
      Focused = vim.bo.buftype,
    })
    vim.cmd("qa!")
  end)
end })
`
		runLua(t, root, append([]string{"--clean"}, args...), lua, &got)

		if !slices.Equal(got.Cmd, []string{wait}) {
			t.Errorf("g:xf_cmd = %q", got.Cmd)
		}
		if !slices.Equal(got.LSPCmd, []string{fail, "lsp"}) || !got.Enabled {
			t.Errorf("language server cmd = %q, enabled %v", got.LSPCmd, got.Enabled)
		}
		if got.Wins != 2 || got.Editor != c.wantEditor || got.Focused != "" {
			t.Errorf("files %q: windows = %d, editor shows %q, focus in a %q buffer; want 2, %q, the editor focused",
				c.files, got.Wins, got.Editor, got.Focused, c.wantEditor)
		}
	}
}

// TestEditRemote opens a file from outside neovim the way xf's o does.
func TestEditRemote(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("the :Xf split isn't supported on Windows")
	}
	nvim := nvimBin(t)
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	standIns(t)
	sock := filepath.Join(testutil.ShortTempDir(t), "s")
	if filepath.Separator == '\\' { // Windows: neovim listens on a named pipe
		sock = `\\.\pipe\xf-nvim-test-` + filepath.Base(t.TempDir())
	}
	srv := exec.Command(nvim, "--headless", "-u", "NONE", "-i", "NONE", "-n", "--listen", sock,
		"--cmd", "set rtp^="+dir, "-c", "runtime plugin/xf.lua",
		"-c", `let g:xf_cmd = [$XF_TEST_WAIT]`, "-c", "Xf")
	srv.Env = append(os.Environ(), "NVIM=")
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill(); _ = srv.Wait() })

	expr := func(e string) string {
		t.Helper()
		out, err := exec.Command(nvim, "--server", sock, "--remote-expr", e).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", e, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			if out, err := exec.Command(nvim, "--server", sock, "--remote-expr", "&buftype").Output(); err == nil && strings.TrimSpace(string(out)) == "terminal" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("neovim never focused xf")
		}
		time.Sleep(50 * time.Millisecond)
	}

	file := projectFile(t)
	out, err := exec.Command(nvim, EditArgs(sock, file, 2)...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := expr(`expand("%:p")`); got != file {
		t.Errorf("current file = %q, want %q", got, file)
	}
	if got := expr(`line(".")`); got != "3" {
		t.Errorf("cursor on line %s, want 3", got)
	}
	if got := expr("&buftype"); got != "" {
		t.Errorf("focus is in a %q buffer, want the editor", got)
	}
	if got := expr("winnr('$')"); got != "2" {
		t.Errorf("windows = %s, want 2", got)
	}
}

func TestInstall(t *testing.T) {
	cache := t.TempDir()
	a, err := Install(cache)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Install(cache)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("installed twice: %s, %s", a, b)
	}
	for _, f := range []string{"plugin/xf.lua", "lsp/xf.lua", "lua/xf.lua"} {
		if _, err := os.Stat(filepath.Join(a, f)); err != nil {
			t.Error(err)
		}
	}
	entries, _ := os.ReadDir(cache)
	if len(entries) != 1 {
		t.Errorf("cache has %d entries, want 1", len(entries))
	}
}

func TestEditArgsQuotes(t *testing.T) {
	got := EditArgs("/tmp/s", "/p/it's.sqlx", 4)
	want := []string{"--server", "/tmp/s", "--remote-expr", `luaeval("require'xf'.edit(_A[1], _A[2])", ['/p/it''s.sqlx', 4])`}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestTermEnv starts neovim as xf web's terminal tab would have it, with
// no config of its own: it loads xf's plugin and enables the language
// server, and opens no split.
func TestTermEnv(t *testing.T) {
	rt, err := Install(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wait, fail := standIns(t)
	env, err := TermEnv(rt, []string{wait}, []string{fail, "lsp"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	var got struct {
		Xf      bool
		LSPCmd  []string
		Enabled bool
		Wins    int
	}
	lua := `
vim.api.nvim_create_autocmd("VimEnter", { once = true, callback = function()
  vim.schedule(function()
    report({
      Xf = vim.fn.exists(":Xf") == 2,
      LSPCmd = vim.lsp.config.xf and vim.lsp.config.xf.cmd or {},
      Enabled = vim.lsp.is_enabled("xf"),
      Wins = #vim.api.nvim_tabpage_list_wins(0),
    })
    vim.cmd("qa!")
  end)
end })
`
	runLua(t, t.TempDir(), nil, lua, &got)
	if !got.Xf || !got.Enabled || !slices.Equal(got.LSPCmd, []string{fail, "lsp"}) || got.Wins != 1 {
		t.Errorf("plugin loaded %v, language server %q enabled %v, %d windows; want true, [%s lsp], true, 1",
			got.Xf, got.LSPCmd, got.Enabled, got.Wins, fail)
	}
}

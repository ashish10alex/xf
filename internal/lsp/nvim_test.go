package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/engine"
)

// fakeLSPEnv makes the test binary a language server that writes the
// active files it is told about to the file the variable names.
const fakeLSPEnv = "XF_TEST_LSP_OUT"

// fakeShowEnv, as "file:line", makes that language server ask the editor
// to open file at line once it is told of an active file with the event
// "Show", and write how it went.
const fakeShowEnv = "XF_TEST_LSP_SHOW"

func TestMain(m *testing.M) {
	if out := os.Getenv(fakeLSPEnv); out != "" {
		os.Exit(fakeLSP(out))
	}
	os.Exit(m.Run())
}

// recordingEngine appends each active file to a file, and asks the editor
// to open show (fakeShowEnv's) when told to.
type recordingEngine struct {
	*engine.Engine
	mu   sync.Mutex
	out  string
	show string
	open func(ctx context.Context, file string, line int) error
}

func (e *recordingEngine) ActiveFile(event, file string) {
	e.record(fmt.Sprintf("%s\t%s", event, file))
	e.mu.Lock()
	open := e.open
	e.mu.Unlock()
	if event != "Show" || e.show == "" {
		return
	}
	i := strings.LastIndex(e.show, ":")
	line, _ := strconv.Atoi(e.show[i+1:])
	// Not on the message loop, which has to read the editor's answer.
	go func() {
		err := errors.New("the server can't show documents")
		if open != nil {
			err = open(context.Background(), e.show[:i], line)
		}
		e.record(fmt.Sprintf("shown\t%v", err))
	}()
}

func (e *recordingEngine) HandleShowDocument(open func(ctx context.Context, file string, line int) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.open = open
}

func (e *recordingEngine) record(line string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	f, err := os.OpenFile(e.out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, line)
	_ = f.Close()
}

func fakeLSP(out string) int {
	cfg := Config{Connect: func(ctx context.Context, _ string) (Engine, error) {
		eng := engine.New(engine.Config{})
		_ = eng.Start(ctx)
		return &recordingEngine{Engine: eng, out: out, show: os.Getenv(fakeShowEnv)}, nil
	}}
	if err := Serve(context.Background(), os.Stdin, os.Stdout, cfg); err != nil {
		return 1
	}
	return 0
}

// TestNvimPlugin runs the real plugin and language server config in headless
// neovim, with this test binary as the language server, and checks that the
// active files reach the engine.
func TestNvimPlugin(t *testing.T) {
	nvim, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim not on PATH")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	nvimDir, err := filepath.Abs(filepath.Join("..", "..", "nvim"))
	if err != nil {
		t.Fatal(err)
	}
	root := testdata(t, "projects", "navigation")
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	settings := filepath.Join(root, "workflow_settings.yaml")
	outside := filepath.Join(t.TempDir(), "notes.sqlx")
	if err := os.WriteFile(outside, []byte("select 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbtRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(dbtRoot, "models", "a.sql")
	for file, text := range map[string]string{filepath.Join(dbtRoot, "dbt_project.yml"): "name: a\n", model: "select 1\n"} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "active")

	script := filepath.Join(t.TempDir(), "run.lua")
	lua := fmt.Sprintf(`
vim.opt.rtp:append(%q)
vim.cmd("filetype on")
vim.cmd("runtime plugin/xf.lua")
vim.lsp.config("xf", { cmd = { %q } })
vim.lsp.enable("xf")
-- reached waits until the server has written that file was entered: the
-- server is this test binary, which can take seconds to start under load
-- (and on Windows, while Defender scans it).
local out = %q
local function reached(file)
  vim.wait(30000, function()
    local f = io.open(out)
    if not f then return false end
    local got = f:read("*a")
    f:close()
    return got:find("BufEnter\t" .. file .. "\n", 1, true) ~= nil
  end)
end
vim.cmd.edit(%q) -- not a Dataform project: nothing to tell
vim.cmd.edit(%q)
reached(vim.fn.expand("%%:p"))
vim.cmd.edit(%q) -- not attached, but in the same project
reached(vim.fn.expand("%%:p"))
vim.cmd.edit(%q) -- a dbt model
reached(vim.fn.expand("%%:p"))
vim.cmd("qa!")
`, nvimDir, self, out, outside, report, settings, model)
	if err := os.WriteFile(script, []byte(lua), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(nvim, "--headless", "-u", "NONE", "-i", "NONE", "-n", "-c", "luafile "+script)
	cmd.Env = append(os.Environ(), fakeLSPEnv+"="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nvim: %v\n%s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("no active files reached the server: %v", err)
	}
	got := string(data)
	for _, want := range []string{"BufEnter\t" + report + "\n", "BufEnter\t" + settings + "\n", "BufEnter\t" + model + "\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, outside) {
		t.Errorf("a file outside the project was sent:\n%s", got)
	}
}

// TestNvimShowsDocuments has the language server ask neovim, with the real
// plugin and config, to open a file at a line (the TUI's o key): over the
// current window, or with xf in a split, in the editor window beside it.
func TestNvimShowsDocuments(t *testing.T) {
	nvim, err := exec.LookPath("nvim")
	if err != nil {
		t.Skip("nvim not on PATH")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	nvimDir, err := filepath.Abs(filepath.Join("..", "..", "nvim"))
	if err != nil {
		t.Fatal(err)
	}
	root := testdata(t, "projects", "navigation")
	report := filepath.Join(root, "definitions", "marts", "report.sqlx")
	target := filepath.Join(root, "definitions", "staging", "stg_events.sqlx")
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprintf("split=%v", split), func(t *testing.T) {
			if split && filepath.Separator == '\\' {
				t.Skip("the :Xf split isn't supported on Windows")
			}
			out := filepath.Join(t.TempDir(), "active")
			result := filepath.Join(t.TempDir(), "result.json")
			script := filepath.Join(t.TempDir(), "run.lua")
			lua := fmt.Sprintf(`
vim.opt.rtp:append(%q)
vim.cmd("filetype on")
vim.cmd("runtime plugin/xf.lua")
vim.lsp.config("xf", { cmd = { %q } })
vim.lsp.enable("xf")
local out = %q
local function seen(text)
  return vim.wait(30000, function()
    local f = io.open(out)
    if not f then return false end
    local got = f:read("*a")
    f:close()
    return got:find(text, 1, true) ~= nil
  end)
end
vim.cmd.edit(%q)
seen("BufEnter\t")
if %v then
  vim.g.xf_cmd = { "sh", "-c", "read x" }
  vim.cmd("Xf") -- focuses xf
end
local c = vim.lsp.get_clients({ name = "xf" })[1]
c:notify("xf/activeFile", { event = "Show", file = %q })
local shown = seen("shown\t")
local f = io.open(%q, "w")
f:write(vim.json.encode({
  shown = shown,
  file = vim.fn.expand("%%:p"),
  line = vim.fn.line("."),
  buftype = vim.bo.buftype,
  windows = vim.fn.winnr("$"),
  xf = require("xf").has_split(),
}))
f:close()
vim.cmd("qa!")
`, nvimDir, self, out, report, split, report, result)
			if err := os.WriteFile(script, []byte(lua), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(nvim, "--headless", "-u", "NONE", "-i", "NONE", "-n", "-c", "luafile "+script)
			cmd.Env = append(os.Environ(), fakeLSPEnv+"="+out, fakeShowEnv+"="+target+":4", "NVIM=")
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("nvim: %v\n%s", err, b)
			}
			data, err := os.ReadFile(result)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Shown   bool
				File    string
				Line    int
				Buftype string
				Windows int
				Xf      bool
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("%v: %s", err, data)
			}
			log, _ := os.ReadFile(out)
			if !got.Shown || !strings.Contains(string(log), "shown\t<nil>") {
				t.Fatalf("the editor didn't say it showed the file:\n%s", log)
			}
			if got.File != target || got.Line != 5 || got.Buftype != "" {
				t.Errorf("at %s:%d (buftype %q), want %s:5 in an editor window", got.File, got.Line, got.Buftype, target)
			}
			if split && (got.Windows != 2 || !got.Xf) {
				t.Errorf("%d windows, xf split %v; want xf and the editor", got.Windows, got.Xf)
			}
		})
	}
}

-- xf in a split beside the editor. plugin/xf.lua makes :Xf call
-- M.open; the xf running in that split calls M.edit through $NVIM (see
-- package nvim) when you press o on an action, and M.toggle_full_screen
-- when you press Z.

local M = {}

-- width is the share of the columns the xf split takes.
M.width = 0.5

-- width is the xf split's width in columns.
local function width()
  return math.max(20, math.floor(vim.o.columns * M.width))
end

local function is_xf(buf)
  return vim.api.nvim_buf_is_valid(buf) and vim.b[buf].xf_term == true
end

-- find_xf_win returns xf's split in this tab, not its full-screen float.
local function find_xf_win()
  for _, win in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
    if is_xf(vim.api.nvim_win_get_buf(win)) and vim.api.nvim_win_get_config(win).relative == "" then
      return win
    end
  end
end

-- has_split reports whether xf is open in a split in this tab.
function M.has_split()
  return find_xf_win() ~= nil
end

-- full_win is the float showing xf full screen, if there is one, and
-- full_from the window it covers, where xf goes back to.
local full_win, full_from

local function full_screen()
  return full_win ~= nil and vim.api.nvim_win_is_valid(full_win)
end

local function full_size()
  return { relative = "editor", row = 0, col = 0, width = vim.o.columns, height = math.max(1, vim.o.lines - vim.o.cmdheight) }
end

-- leave_full_screen closes the float, leaving xf in its split (the
-- terminal takes the split's size again).
local function leave_full_screen()
  pcall(vim.api.nvim_del_augroup_by_name, "xf_full_screen")
  if full_screen() then
    pcall(vim.api.nvim_win_close, full_win, true)
  end
  full_win = nil
end

-- cmd is the xf to run: vim.g.xf_cmd (a list, or a string), else xf on
-- PATH. xf --nvim sets it to itself.
local function cmd()
  local c = vim.g.xf_cmd
  if type(c) == "string" then
    return { c }
  end
  if type(c) == "table" and #c > 0 then
    return vim.deepcopy(c)
  end
  return { "xf" }
end

-- lock keeps xf's buffer in win: :bnext, :edit and the like are refused
-- there (E1513) instead of putting a file in xf's place, which would wipe
-- the terminal and stop xf.
local function lock(win)
  if vim.fn.exists("+winfixbuf") == 1 then
    vim.wo[win][0].winfixbuf = true
  end
end

local function start_term(argv, cwd)
  if vim.fn.has("nvim-0.11") == 1 then
    return vim.fn.jobstart(argv, { term = true, cwd = cwd })
  end
  return vim.fn.termopen(argv, { cwd = cwd })
end

-- setup_term makes the xf buffer behave like a pane: keys go to xf when
-- its window is entered, <C-w> commands still move between windows,
-- :bnext and :bprevious in the editor skip it, and the window closes when
-- xf quits.
local function setup_term(buf)
  vim.b[buf].xf_term = true
  vim.bo[buf].buflisted = false
  vim.keymap.set("t", "<C-w>", [[<C-\><C-n><C-w>]], { buffer = buf, desc = "xf: window command" })
  local group = vim.api.nvim_create_augroup("xf_term_" .. buf, { clear = true })
  vim.api.nvim_create_autocmd({ "BufEnter", "WinEnter" }, {
    group = group,
    buffer = buf,
    callback = function()
      vim.cmd.startinsert()
    end,
  })
  vim.api.nvim_create_autocmd("TermClose", {
    group = group,
    buffer = buf,
    callback = function()
      vim.schedule(function()
        for _, win in ipairs(vim.fn.win_findbuf(buf)) do
          if #vim.api.nvim_list_wins() > 1 then
            pcall(vim.api.nvim_win_close, win, true)
          end
        end
        if vim.api.nvim_buf_is_valid(buf) and #vim.fn.win_findbuf(buf) == 0 then
          pcall(vim.api.nvim_buf_delete, buf, { force = true })
        end
      end)
    end,
  })
end

-- open shows xf in a split on the right, running from the current
-- buffer's project (or the working directory), and focuses it, ready for
-- keys; if it is already open in this tab it is only focused. With
-- opts.focus false the focus stays where it was.
function M.open(opts)
  local focus = not opts or opts.focus ~= false
  local win = full_screen() and full_win or find_xf_win()
  if win then
    if focus then
      vim.api.nvim_set_current_win(win)
      vim.cmd.startinsert()
    end
    return
  end
  local cwd = vim.fs.root(0, { "workflow_settings.yaml", "dataform.json", "dbt_project.yml" }) or vim.fn.getcwd()
  local from = vim.api.nvim_get_current_win()
  vim.cmd("botright vnew")
  local buf = vim.api.nvim_get_current_buf()
  vim.bo[buf].bufhidden = "wipe"
  vim.api.nvim_win_set_width(0, width())
  vim.wo.winfixwidth = true
  local ok, job = pcall(start_term, cmd(), cwd)
  if not ok or job <= 0 then
    vim.api.nvim_win_close(0, true)
    vim.notify("xf: could not start " .. table.concat(cmd(), " ") .. (ok and "" or (": " .. job)), vim.log.levels.ERROR)
    return
  end
  setup_term(buf)
  lock(vim.api.nvim_get_current_win())
  if focus then
    vim.cmd.startinsert()
  else
    vim.api.nvim_set_current_win(from)
  end
end

-- editor_win returns the window to open files in: the previous window if
-- it shows a normal buffer, else the first such window in this tab. Failing
-- those it returns a file explorer's window (nvim-tree taking over the
-- directory xf --nvim opened, say) and true.
local function editor_win()
  local function tiled(win)
    return win ~= 0
      and vim.api.nvim_win_is_valid(win)
      and vim.api.nvim_win_get_config(win).relative == ""
      and not is_xf(vim.api.nvim_win_get_buf(win))
  end
  local function normal(win)
    return tiled(win) and vim.bo[vim.api.nvim_win_get_buf(win)].buftype == ""
  end
  local prev = vim.fn.win_getid(vim.fn.winnr("#"))
  if normal(prev) then
    return prev, false
  end
  local wins = vim.api.nvim_tabpage_list_wins(0)
  for _, win in ipairs(wins) do
    if normal(win) then
      return win, false
    end
  end
  for _, win in ipairs(wins) do
    if tiled(win) then
      return win, true
    end
  end
end

-- edit opens file in the editor window beside xf, at line (0-based, when
-- given), and focuses it. A file explorer's window is replaced, as picking
-- the file there would (explorers such as nvim-tree don't let another
-- buffer into their window), and with no window left of xf one is made.
function M.edit(file, line)
  leave_full_screen()
  local xf = find_xf_win()
  local win, explorer = editor_win()
  if win then
    vim.api.nvim_set_current_win(win)
    if explorer then
      vim.cmd("rightbelow vnew")
      pcall(vim.api.nvim_win_close, win, false)
    end
  else
    vim.cmd("topleft vnew")
  end
  vim.cmd.stopinsert()
  vim.cmd.edit(vim.fn.fnameescape(file))
  if line then
    local last = vim.api.nvim_buf_line_count(0)
    pcall(vim.api.nvim_win_set_cursor, 0, { math.min(line + 1, last), 0 })
  end
  -- making and closing windows (and explorers reacting to it) can resize xf
  if xf and vim.api.nvim_win_is_valid(xf) then
    vim.api.nvim_win_set_width(xf, width())
  end
  return true
end

-- toggle_full_screen shows xf over the whole editor, in a float over its
-- split, or puts it back in the split: the Z key in xf calls it. Leaving
-- the float for another window puts xf back too, so the editor is never
-- hidden behind it. The xf whose Z calls this is the current window,
-- which also covers a :terminal xf opened by hand.
function M.toggle_full_screen()
  if full_screen() then
    leave_full_screen()
    if full_from and vim.api.nvim_win_is_valid(full_from) then
      vim.api.nvim_set_current_win(full_from)
      vim.cmd.startinsert()
    end
    return true
  end
  local win = vim.bo.buftype == "terminal" and vim.api.nvim_get_current_win() or find_xf_win()
  if not win then
    error("no xf window to show full screen")
  end
  full_from = win
  local cfg = full_size()
  cfg.style = "minimal"
  full_win = vim.api.nvim_open_win(vim.api.nvim_win_get_buf(win), true, cfg)
  lock(full_win)
  local group = vim.api.nvim_create_augroup("xf_full_screen", { clear = true })
  vim.api.nvim_create_autocmd("WinLeave", {
    group = group,
    callback = function()
      if vim.api.nvim_get_current_win() == full_win then
        vim.schedule(leave_full_screen)
      end
    end,
  })
  vim.api.nvim_create_autocmd("VimResized", {
    group = group,
    callback = function()
      if full_screen() then
        vim.api.nvim_win_set_config(full_win, full_size())
      end
    end,
  })
  vim.cmd.startinsert()
  return true
end

-- ensure_editor_win makes an empty window right of a file explorer (such
-- as nvim-tree taking over the directory xf --nvim opened) when no other
-- window can take a file. Explorers don't open files in terminals, so
-- without one they rearrange windows to make room, moving xf.
local function ensure_editor_win()
  local win, explorer = editor_win()
  if not win or not explorer then
    return
  end
  local xf = find_xf_win()
  local from = vim.api.nvim_get_current_win()
  -- explorers fix their window's width, which would leave this one a column
  local fixed = vim.wo[win].winfixwidth
  vim.wo[win].winfixwidth = false
  vim.api.nvim_set_current_win(win)
  vim.cmd("rightbelow vnew")
  vim.bo.bufhidden = "wipe"
  vim.wo[win].winfixwidth = fixed
  if xf then
    vim.api.nvim_win_set_width(xf, width())
  end
  vim.api.nvim_set_current_win(from)
end

-- attach loads the plugin from opts.runtime when the user's config
-- hasn't, and enables the language server with xf's own command when the
-- user's config hasn't. A neovim in xf web's terminal tab runs it
-- (site/nvim/site/plugin/xf_terminal.lua). opts: { runtime, cmd, lsp_cmd }.
function M.attach(opts)
  vim.g.xf_cmd = opts.cmd
  if vim.fn.exists(":Xf") ~= 2 then
    vim.opt.rtp:prepend(opts.runtime)
    vim.g.loaded_xf = nil
    vim.cmd.source(vim.fn.fnameescape(opts.runtime .. "/plugin/xf.lua"))
  end
  local enabled
  if vim.lsp.is_enabled then
    enabled = vim.lsp.is_enabled("xf")
  else
    enabled = vim.lsp._enabled_configs ~= nil and vim.lsp._enabled_configs.xf ~= nil
  end
  if not enabled and vim.lsp.config then
    vim.lsp.config("xf", { cmd = opts.lsp_cmd })
    vim.lsp.enable("xf")
  end
end

-- launch is what xf --nvim runs once nvim has started: attach, then the
-- split, leaving the focus in the editor.
function M.launch(opts)
  M.attach(opts)
  M.open({ focus = false })
  ensure_editor_win()
end

return M

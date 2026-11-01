-- xf follows the active buffer: tell the xf language server
-- (lsp/xf.lua) which file is active, and it tells every xf running for the
-- project. Does nothing until the language server is attached, so turn it on
-- with vim.lsp.enable("xf"); it also gives go to definition, hovers,
-- completion and diagnostics, in Dataform and dbt projects.
--
-- :Xf opens xf in a split on the right (lua/xf.lua), where o opens the
-- selected action's file back in the editor. `xf --nvim` starts neovim
-- with that split open, loading this plugin if your config doesn't.
--
-- Install with lazy.nvim:  { dir = "/path/to/xf/nvim" }
-- or:                      vim.opt.rtp:append("/path/to/xf/nvim")

if vim.g.loaded_xf then
  return
end
vim.g.loaded_xf = true

vim.filetype.add({ extension = { sqlx = "sqlx" } })

local uv = vim.uv or vim.loop

-- clients returns the xf language servers for buf's project: the one
-- attached to it, or for a file it doesn't attach to (workflow_settings.yaml
-- or a dbt schema.yml, say) the one serving the same project.
local function clients(buf)
  local attached = vim.lsp.get_clients({ bufnr = buf, name = "xf" })
  if #attached > 0 then
    return attached
  end
  local root = vim.fs.root(buf, { "workflow_settings.yaml", "dataform.json", "dbt_project.yml" })
  if not root then
    return {}
  end
  root = uv.fs_realpath(root) or root
  local out = {}
  for _, c in ipairs(vim.lsp.get_clients({ name = "xf" })) do
    if c.root_dir and (uv.fs_realpath(c.root_dir) or c.root_dir) == root then
      table.insert(out, c)
    end
  end
  return out
end

local function notify(event, buf)
  if not vim.api.nvim_buf_is_valid(buf) or vim.bo[buf].buftype ~= "" then
    return
  end
  local name = vim.api.nvim_buf_get_name(buf)
  if name == "" then
    return
  end
  local file = uv.fs_realpath(name) or name
  for _, c in ipairs(clients(buf)) do
    c:notify("xf/activeFile", { event = event, file = file })
  end
end

vim.api.nvim_create_user_command("Xf", function()
  require("xf").open()
end, { desc = "Open xf in a split, or focus it" })

local group = vim.api.nvim_create_augroup("xf", { clear = true })

vim.api.nvim_create_autocmd({ "BufEnter", "FocusGained", "CursorHold" }, {
  group = group,
  callback = function(ev)
    notify(ev.event, ev.buf)
  end,
})

local warned = false

-- warn_stale says, once, that the xf on PATH is older than this plugin and
-- what that costs.
local function warn_stale(cost)
  if warned then
    return
  end
  warned = true
  vim.notify(
    "xf: the xf on PATH is older than this plugin, so " .. cost .. "." .. " Run `just install` in the xf repo, then :lsp restart",
    vim.log.levels.WARN
  )
end

-- The first buffer of a project is entered before its server attaches, so
-- say so again once it has. A xf on PATH older than this plugin may not take
-- the active file, or serve dbt projects: say how to update it, once.
vim.api.nvim_create_autocmd("LspAttach", {
  group = group,
  callback = function(ev)
    local c = vim.lsp.get_client_by_id(ev.data.client_id)
    if not c or c.name ~= "xf" then
      return
    end
    local exp = (c.server_capabilities or {}).experimental or {}
    if not exp.xfActiveFile then
      warn_stale("xf won't follow your buffers")
      return
    end
    if not exp.xfDbt and c.root_dir and uv.fs_stat(vim.fs.joinpath(c.root_dir, "dbt_project.yml")) then
      warn_stale("it gives no jumps, hovers, completion or diagnostics in dbt projects")
    end
    if ev.buf == vim.api.nvim_get_current_buf() then
      notify("BufEnter", ev.buf)
    end
  end,
})

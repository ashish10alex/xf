-- A neovim started in xf web's terminal tab finds this file: the tab's
-- shell puts its directory first in XDG_DATA_DIRS and sets $XF_NVIM to
-- { runtime, cmd, lsp_cmd }. Once neovim has started it loads xf's plugin
-- and enables xf's language server, each unless your config already did,
-- as `xf --nvim` does, without opening the :Xf split.
local opts = vim.env.XF_NVIM
if not opts or opts == "" then
  return
end
local ok, o = pcall(vim.json.decode, opts)
if not ok or type(o) ~= "table" or not o.runtime then
  return
end
vim.api.nvim_create_autocmd("VimEnter", {
  once = true,
  nested = true,
  callback = function()
    dofile(o.runtime .. "/lua/xf.lua").attach(o)
  end,
})

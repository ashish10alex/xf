-- xf lsp: go to definition, hovers, completion and diagnostics for Dataform
-- and dbt projects. It shares its compiles and dry runs with a running xf
-- through a background `xf serve`, and passes on the active file (see
-- plugin/xf.lua). Enable it with vim.lsp.enable("xf") once this directory is
-- on the runtimepath (see plugin/xf.lua). Needs `xf` on PATH (`just install`).
--
-- It attaches to .sqlx and JavaScript files only inside a Dataform project,
-- so plain JavaScript elsewhere is left to your usual language server, and
-- to SQL only inside a dbt project, under the filetypes dbt plugins give it
-- too.

-- dbt_filetypes are the filetypes a dbt project's SQL may have.
local dbt_filetypes = { sql = true, ["sql.jinja"] = true, ["jinja.sql"] = true, dbt = true }

-- show_document opens the files xf's o key asks for (window/showDocument).
-- With xf in a split beside this neovim (:Xf), they open in its editor
-- window, as the split's own o does, rather than over xf; otherwise
-- neovim's own handler opens them.
local function show_document(err, result, ctx, config)
  local ok, xf = pcall(require, "xf")
  if not err and result and ok and xf.has_split() and vim.startswith(result.uri, "file:") then
    xf.edit(vim.uri_to_fname(result.uri), result.selection and result.selection.start.line)
    return { success = true }
  end
  return vim.lsp.handlers["window/showDocument"](err, result, ctx, config)
end

---@type vim.lsp.Config
return {
  cmd = { "xf", "lsp" },
  handlers = { ["window/showDocument"] = show_document },
  filetypes = { "sqlx", "javascript", "sql", "sql.jinja", "jinja.sql", "dbt" },
  root_dir = function(bufnr, on_dir)
    local markers = { "workflow_settings.yaml", "dataform.json" }
    if dbt_filetypes[vim.bo[bufnr].filetype] then
      markers = { "dbt_project.yml" }
    end
    local root = vim.fs.root(bufnr, markers)
    if root then
      on_dir(root)
    end
  end,
}

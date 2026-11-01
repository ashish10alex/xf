# xf with neovim

There are two ways to use xf with neovim.

## Quickest: `xf --nvim`

```bash
xf --nvim
```

This opens neovim in the project with xf in a split on the right, and turns on the language server for that session. You don't need to install anything in your neovim config for this. You can pass files as well: `xf --nvim definitions/orders.sqlx`.

## Permanent: install the plugin

Add the plugin to your config so that `:Xf` and the language server work in every neovim session. The plugin is the `nvim/` directory of the repo you cloned.

With [lazy.nvim](https://github.com/folke/lazy.nvim):

```lua
{
  dir = "~/path/to/xf/nvim",
  config = function()
    vim.lsp.enable("xf")
  end,
}
```

Without a plugin manager, in `init.lua`:

```lua
nvim.opt.rtp:append("~/path/to/xf/nvim")
nvim.lsp.enable("xf")
```

The plugin:

- registers the `sqlx` filetype,
- adds the `xf` language server, which gives go-to-definition on `ref()`, hovers with table details, completion and diagnostics (including dry-run errors). It attaches to `.sqlx` and JavaScript files only inside a Dataform project, and to SQL files only inside a dbt project (see [dbt projects](dbt.md#dbt-projects)), so your usual JavaScript and SQL servers are left alone elsewhere.
- adds `:Xf`, which opens xf in a split beside the editor, or focuses it if it's already open.

Restart neovim, open a `.sqlx` file in a Dataform project (or a model in a dbt project), and run `:checkhealth vim.lsp` to confirm `xf` is attached.

## The loop

With xf beside neovim:

- **Save a file** and xf recompiles; its panes and your diagnostics update.
- **Switch buffers** and xf selects the action for the file you're in.
- **Press `o`** on an action in xf to open its file in the editor window.
- **Press `Z`** to toggle xf between its split and full screen.
- **`<C-w>`** moves between windows from inside the xf split, as it does elsewhere in neovim.
- **`:bnext` and `:bprevious`** skip xf, and its split keeps its buffer: a buffer command typed there fails with `E1513` instead of replacing xf.

The TUI and the language server share one background engine (`xf serve`) when they compile with the same `dataform` binary (or, for dbt, the same `dbt` binary and target). It starts when either of them needs it and stops a few seconds after the last one quits, so a save is compiled once for both. After an upgrade, the first xf of the new build to connect stops an engine of an older one and starts its own; `xf stop` stops every engine you run and lists the xf processes still running. It also saves each clean compile, so xf starts with its SQL on screen instead of waiting for `dataform compile`: while nothing it was compiled from has changed (`definitions/`, `includes/`, the settings and package files, the Dataform CLI, the day), it isn't compiled again; otherwise it shows as `outdated` while a fresh compile replaces it. Press `c` to recompile anyway. dbt compiles are saved the same way (see [dbt projects](dbt.md#dbt-projects)).

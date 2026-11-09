---
status: accepted
---

# xf web checks Python files with pyrefly

In xf web, a `.py` file in the source pane gets pyrefly's language server (`pyrefly lsp`): its errors marked in the file, `K` (or the mouse) for its hover, `gd` to a definition, completion, and signature help above a call being written. pyrefly is the only Python language server xf runs.

- **One server per project.** xf web's Go server starts `pyrefly lsp` the first time a page opens a `.py` file in the project, and stops it when the project closes or xf web exits.
  A project with no Python never runs it.
  The page talks to it through `/api/python/…`; its errors reach the page as `python` events over the WebSocket (ADR 0011).
- **As you type.**
  The editor's text goes to pyrefly a moment (300 ms) after each edit, unsaved, and its errors come back for that text.
  They show in the open file only: the errors tab stays the compile's.
- **The interpreter.**
  *Python: select interpreter* (in the ⌘P commands) lists the interpreters xf finds: a `.venv/`, `venv/` or `env/` at the project root, `$VIRTUAL_ENV`, `python3` on PATH, and *Enter path…*.
  The status line shows the one in use while a `.py` file is open; a click on it opens the list.
  Until one is picked, the project root's `.venv/` (then `venv/`) is used if there is one, else pyrefly's own default.
  The pick is kept in xf's session for the project (per user, outside the repo) and handed to pyrefly as the `python` section's `pythonPath` of `workspace/configuration`, as VS Code does; changing it doesn't restart pyrefly.
- **The config.** pyrefly reports no type errors for a project without a config.
  The first time a `.py` file opens in a project with neither a `pyrefly.toml` nor a `[tool.pyrefly]` in a `pyproject.toml`, xf offers to write `pyrefly.toml` at the project root: `project-includes = ["**/*.py"]`, and excludes for `target/`, `dbt_packages/`, `node_modules/`, `.venv/`, `venv/` and `logs/`.
  It holds no interpreter.
  Declined, the offer is kept in the session and not made again; *Python: create pyrefly config* still writes it. xf never edits a `pyproject.toml`.
- **No pyrefly.** pyrefly is looked for in the interpreter's `bin/`, then on PATH.
  Missing, the page says once per project `pyrefly not found: uv tool install pyrefly`, and the file shows with highlighting only. xf never installs it.
- **Off.** The `pythonLanguageServer` key (user or project) is `"pyrefly"`, the default, or `"off"`.
- **Library files.**
  `gd` into a library (the standard library, a venv's `site-packages`, a `.venv/` inside the project) shows the file in the source pane, read-only, at the definition.
  The pane opens a file outside the project only when pyrefly named it.

The TUI is in maintenance mode and gets none of this; `xf lsp` neither: an editor that runs it runs its own Python server.

## Why

A dbt project's Python models, and the scripts beside a Dataform project, had highlighting and nothing else. pyrefly is fast enough to check a file at every pause in typing, and speaks LSP over stdio, so the Go server can run it as it runs `dataform` and `git`.
The page already has the marks, cards, `gd` and completion that SQL uses; Python fills them from another source.

A venv's path belongs to one machine, so it isn't written into `pyrefly.toml` or `.xf/config.json`, which are committed. pyrefly asks its client for the interpreter, so the session can hold it.
Without a config pyrefly checks nothing, so a config is what turns the errors on; but a file in the repo nobody asked for would show up in Source Control, so xf asks first.

## Considered options

- **Diagnostics and hover only, or everything pyrefly offers** (references, rename, code actions, inlay hints).
  The first leaves `gd` and completion out, which SQL files have; the second needs UI the pane doesn't have.
- **The interpreter in `pyrefly.toml` or `.xf/config.json`.**
  The pyrefly CLI and other editors would see it, but an absolute path from one machine would be committed.
- **Writing the config without asking, never offering it, or never writing one.**
  Silent puts an untracked file in Source Control; the palette alone leaves most projects without errors; none leaves them all without.
- **`[tool.pyrefly]` in `pyproject.toml`, or running `pyrefly init`.**
  The first edits a file the user keeps; the second includes `**/*.py*` and notebooks with nothing excluded, so a dbt project's `dbt_packages/` and `target/` are checked too.
- **Asking for the interpreter on the first `.py` file, or leaving it to pyrefly.** A second prompt beside the config's, or a project's `.venv/` missed.
- **Errors in the errors tab too.** It is the compile's; a project's type errors can be hundreds.
- **Errors on save.** A fixed error would stay marked until the save, and hover, `gd` and completion need the unsaved text anyway.
- **Installing pyrefly, an idle timeout, starting it with the project.**
  More that can fail on someone's machine; a restart re-indexes; memory for projects with no Python open.
- **Naming the library file in a flash** rather than showing it: `gd` into a library would go nowhere.

## Consequences

- The interpreter indicator is in the status line, beside "following editor": the footer lists keys only, and a flash replaces it.
  The grilling said "footer"; the status line is the bar at the bottom that shows state.
- A dbt Python model's compile errors and pyrefly's show together in the file.
- Two tabs on one file share pyrefly's copy of it: the last to send its text is what pyrefly checks.

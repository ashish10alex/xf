# Checking a change

How to verify a change before calling it done: Go tests, `just jstest`, the TUI in tmux, xf web in the browser preview, and the rules for the user's real BigQuery.

## Go tests

- While working, run the package you touched: `go test ./internal/<pkg>/` (or `just test-race ./internal/<pkg>/` for concurrent code). `just check` (`gofmt`, `go vet`, `GOOS=windows go build ./...`, and `go test ./...`, about 25s) is what pushing `main` runs.
- Keep test paths Windows-portable: build absolute paths with `t.TempDir()` and `filepath.Join` rather than hardcoded Unix paths like `"/foo"`.
- Run heavy jobs one at a time: the full suite, `just check-linux` (Docker) and the Windows VM suite share the Mac's RAM.
- Before a ship, `go test` the packages you touched is enough: `just ship` runs `just check` on the commit, so don't run it yourself first.
- Once the tests pass in the main checkout, `just install` updates `~/go/bin/xf` and `~/.local/bin/xf` and restarts `dev.xf.web`.
  Skip `just install` when another session's uncommitted Go work is in the checkout or when working in a worktree: `just ship` installs the pushed commit.
- A new message the engine sends to clients goes in `internal/remote/wire.go` with `ProtocolVersion` bumped.

## The page's JavaScript

`internal/web/jstest/*.test.js` are `node:test` tests of `internal/web/assets`, run by `TestJS` in `go test ./internal/web/`. Run them directly while working:

```sh
just jstest                       # full JS suite
just jstest scm-blame.test.js     # specific file(s)
```

- `setup.js` gives the modules the browser globals they touch when they load, and pins the platform to a non-Mac.
  `modules.test.js` loads every module, so a broken import fails there.
- Logic that has no DOM gets a test here: key handling, `vim.js` (its cases mirror `internal/ui/buffer_test.go`), diffs, completion ranking.
  CodeMirror's `EditorState` runs in node; `EditorView` needs a browser, so that part stays in the preview.

## The TUI

Drive it in tmux on its own socket (`tmux -L xf-check …`), so it stays off the user's own tmux server and its sessions.
An `xf serve` from an older binary is reused while it lives: check `ps` for `xf serve` first.

## xf web in the browser preview

Start this checkout's preview on a free port with `scripts/preview-bg.sh [dataform|dbt]`, which prints `http://127.0.0.1:<port>/?project=<name>`. Open that URL in the browser preview (in T3 Code: `preview_status`, then `preview_open` / `preview_navigate`), and stop the server when done with `scripts/preview-bg.sh stop <port>`.
(In Claude Desktop's main checkout, `.claude/launch.json` also exposes `xf-dataform`, `xf-dbt`, and `xf-dataform-embedded`.)

`scripts/preview.sh` sets up:

- A scratch git copy of the example under `$TMPDIR/xf-preview/<name>-<port>/`, kept between starts and recreated when it is no longer a project; `XF_PREVIEW_FRESH=1` makes a new one.
- Assets served from this checkout: a JS or CSS edit shows on a reload. A Go edit needs the preview restarted, which rebuilds xf.
- xf's config in `<dir>/xdg/xf/config.json` (for example `{"sourceAutosaveMs": 500}`), read at start: restart after writing it.
- `XF_PREVIEW_EMBEDDED=1` serves the assets built into xf, with their ETags, to measure a release load.

### Reading and driving the page

`await xfAgent()` in the page (via `preview_evaluate`) loads `agent.js` (outside the first load), the page as an agent sees it:

```js
const a = await xfAgent();
a.snapshot();                                  // mode, top layer, focus, selection, source pane, palette, menu, legal commands, last log lines
await a.act('command', 'zoom pane');           // a legal palette command, by its title or a unique part of it
await a.act('key', 'ctrl+shift+p');            // a KeyboardEvent with its code, through main.js's handlers
await a.act('palette', { query: '#orders', pick: 0 });
await a.act('selectAction', 'project.dataset.orders');
await a.act('openFile', 'definitions/orders.sqlx');
await a.act('focus', 'sql');                   // also closeOverlay, pickMenu (a right-click menu's row, by label)
await a.waitFor((s) => s.view?.id === 'project.dataset.orders', 5000);
a.log(20);                                     // the page log's last lines, the agent's steps among them
```

- Each `act` answers `{ ok, blocked, reason, candidates, snapshot }`: an ambiguous title lists the candidates.
- It refuses what costs money or can't be undone (runs, full refresh, Databricks checks, git commit or push, deletes, discards, removing a worktree, closing the project, quitting xf web, keys into the terminal) with `blocked: true`.
  Ask the user, then pass `{ allowDangerous: true }` as its third argument.
- `waitFor` checks after each draw and every 100ms, so it works while the pane is hidden.

### Reading what the page shows

- Read state, not pixels: `snapshot()` above, or `await import(new URL('state.js', location.href))` for the live `S`; `settings.js` gives `SET`.
  Check the DOM via `preview_evaluate` (for example `.cm-activeLine`) over a screenshot, which can lag a render while the pane is hidden.
- Take a screenshot (`preview_snapshot` with `includeImage: true`) only when layout or colour is the point, since images stay in context for the rest of the session.
- `requestAnimationFrame` stops while the pane is hidden. To record something that changes over time, use a `MutationObserver` and `setTimeout`.
- In the source editor, drive loads through `selectAction` (`act('selectAction', id)`) and `discard()`.
  Calling `ed.set()` directly skips `hooks.sourceLoaded`, and the next edit then logs "Mismatched change set lengths".

## BigQuery and GCP

Ask before anything that touches the user's real BigQuery.
Dry runs on `alex-personal-dev-01` are fine without asking; anything that writes needs the user's yes.
Dry runs need the Google login that has access to that project: under another account they fail with 403.

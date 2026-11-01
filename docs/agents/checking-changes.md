# Checking a change

How to see a change working before calling it done: Go tests, the TUI in tmux, xf web in the browser preview, and the rules for the user's real BigQuery.

## Go tests

- While working, run the package you touched: `go test ./internal/<pkg>/`. `just check` (vet + every test, about 20s) is what pushing `main` runs.
- Run heavy jobs one at a time: the full suite, `just check-linux` (Docker) and the Windows VM suite share the Mac's RAM.
- Once the tests pass, `just install`, so `~/go/bin/xf` (which the user and nvim run) is current. When another session's uncommitted Go work is in the checkout, skip it and say so in the wrap-up.
- A new message the engine sends to clients goes in `internal/remote/wire.go` with `ProtocolVersion` bumped.

## The page's JavaScript

`internal/web/jstest/*.test.js` are `node:test` tests of `internal/web/assets`, run by `TestJS` in `go test ./internal/web/`. Run them alone while working:

```sh
node --import ./internal/web/jstest/setup.js --test internal/web/jstest/*.test.js
```

- `setup.js` gives the modules the browser globals they touch when they load, and pins the platform to a non-Mac. `modules.test.js` loads every module, so a broken import fails there.
- Logic that has no DOM gets a test here: key handling, `vim.js` (its cases mirror `internal/ui/buffer_test.go`), diffs, completion ranking. CodeMirror's `EditorState` runs in node; `EditorView` needs a browser, so that part stays in the preview.

## The TUI

Drive it in tmux on its own socket (`tmux -L xf-check …`), so it stays off the user's own tmux server and its sessions. An `xf serve` from an older binary is reused while it lives: check `ps` for `xf serve` first.

## xf web in the browser preview

Start it with `preview_start` and `xf-dataform` (port 7900) or `xf-dbt` (7901), then open `/?project=dataform` (or `dbt`). Both run `scripts/preview.sh`, whose header says what it sets up:

- A scratch git copy of the example under `$TMPDIR/xf-preview/<name>-<port>/`, kept between starts; `XF_PREVIEW_FRESH=1` makes a new one.
- Assets served from the checkout: a JS or CSS edit shows on a reload. A Go edit needs the preview restarted, which rebuilds xf.
- xf's config in `<dir>/xdg/xf/config.json` (for example `{"sourceAutosaveMs": 500}`), read at start: restart after writing it.
- A second preview at the same time needs its own config in `.claude/launch.json` on another port.
- To measure a load as a release serves it, use `xf-dataform-embedded` (7902): the assets built into xf, with their ETags, rather than the checkout's.

### Keys

- `type` inserts text without key events, so vim and xf's keys never see it. Send keys with `key`, one key per token: capitals for shifted keys (`G`, `A`), and `:` or `!` as the key itself.
- Plain text fields (the finder's input): click the field, then `type`.
- `key` sends an empty `code`. Where a handler reads `code`, dispatch a `KeyboardEvent` that carries one from `javascript_tool`.
- The pane denies the clipboard.

### Reading what the page shows

- Read state, not pixels: `await import(new URL('state.js', location.href))` gives the live `S`; `settings.js` gives `SET`. Check the DOM by JS (for example `.cm-activeLine`) over a screenshot, which can lag a render while the pane is hidden.
- `requestAnimationFrame` stops while the pane is hidden. To record something that changes over time, use a `MutationObserver` and `setTimeout`.
- A `navigate` that hangs is an approval prompt waiting on the Mac: tell the user.
- In the source editor, drive loads through `selectAction` and `discard()`. Calling `ed.set()` directly skips `hooks.sourceLoaded`, and the next edit then logs "Mismatched change set lengths".

## BigQuery and GCP

Ask before anything that touches the user's real BigQuery. Dry runs on `alex-personal-dev-01` are fine without asking; anything that writes needs the user's yes. Dry runs need the Google login that has access to that project: under another account they fail with 403.

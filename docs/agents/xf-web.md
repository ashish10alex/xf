# How xf web is built

What the code doesn't say at a glance about `internal/web`. For the Go packages, `go list -f '{{.ImportPath}}: {{.Doc}}' ./...` prints each one's purpose.

## Parity with the TUI

xf web is the TUI in a browser (ADR 0004): the same panes, and the keys of `internal/ui/app.go`. The logic stays in Go, in exported helpers of `internal/ui` (`FindActions`, `ChangedRows`, `RunCommand`, …) that both front ends call; the page draws what it is sent and keeps only view state (selection, focus, scroll, the vim cursor). Where the page goes past the TUI on purpose, an ADR says so (0005, 0006, 0010, 0014, 0016, 0017).

## Server

`internal/web/<feature>.go` holds a feature's `/api/…` handlers, with its test beside it in `<feature>_test.go`. Events reach the page over one WebSocket (ADR 0011). The binary embeds `internal/web/assets`; `XF_WEB_ASSETS=<dir>` serves them from disk instead, which is how the preview picks up edits. Every file is `Cache-Control: no-cache`, so a reload asks again for each one: built-in files carry an ETag of their content (`etag.go`) and files from disk their `Last-Modified`, so an unchanged file is answered 304.

## Page

Native ES modules, no build step:

- `state.js` holds `S`, one mutable object, the server calls (`get`, `post`, `say`) and `handleKey`, which routes each key to the overlay or pane that has it. Change `S`, then call `redraw()`.
- A feature is a pair: `<feature>.js` for its state and keys, `<feature>-view.js` for what it draws (Preact with `htm`, from `vendor/`). `branches.js`, `checks.js`, `errors-tab.js`, `log-tabs.js`, `picker.js` and `sql-pane.js` hold sections that used to be in `state.js`; it re-exports their names, so `view.js`'s `st.` still finds them.
- A module `state.js` imports can import `state.js` back, using its names inside functions only: at load time they aren't set yet. `modules.test.js` fails on a load-time use.
- A module `state.js` doesn't import (Scratch, Source Control, the file tree) fills in `hooks` (`hooks.toggleSCM`, …), which `state.js` calls.
- `vendor/codemirror.mjs` is one bundle built in `scripts/vendor-codemirror` (`npm install && npm run build`); a new CodeMirror package goes in its `entry.js` and `package.json`.

## Page log

When the page stops answering, read `~/.local/state/xf/web-page.log` (`$XDG_STATE_HOME/xf/web-page.log`; the preview's is under its scratch dir's `state/`). `diag.js` notes each key with what had the keys and what it did (`used, drew`, `draw failed`, `to the editor`, …), draws that threw, with their stack, uncaught errors, failed and hanging requests, focus moves, event stream drops and stalls, and sends them every 2s; `pagelog.go` appends them, starting again past 4 MB (the last kept as `.1`). Typed text is never logged: a key into an editor or box is `·`. `xfLog()` in the console prints this page's lines. A draw that throws also shows a red banner, since the page then looks frozen.

## Performance budgets

Two tests in `internal/web` hold the page's speed: `TestFirstLoadBudget` (how many modules, bytes and import levels the first load fetches) and `TestSourceLoadGitCalls` (one git call per source load). When a change needs more, raise the budget in the same commit and say why.

## Where a user-visible change is written down

- Keys: the `?` help (`helpSections` in `view.js`, and the TUI's) and the README.
- Decisions the user made: an ADR in `docs/adr/`.

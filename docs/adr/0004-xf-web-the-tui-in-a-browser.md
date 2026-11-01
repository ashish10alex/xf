---
status: accepted
---

# xf web: the TUI's panes in a browser, worked out in Go

`xf web` serves the TUI as a web page: the same panes, stacked full width in the TUI's order and resizable by dragging (actions, lineage, run, SQL and columns, log with its jobs and history), the same overlays (palette, R, T, I, C, errors, project info, help) and the same keys, with the mouse as well. It is a separate front end, not the terminal UI drawn in a browser terminal.

The server is a client of the project's `xf serve`, as the TUI is, and is given the same `ui.Deps` that `xf` builds for the TUI (`projectDeps` in `cmd/xf`), so it starts runs, dry runs and checks through the same code. What the TUI works out for itself stays in Go and is shared through exported helpers in `internal/ui` (`FindActions`, `ChangedRows`, `RunCommand`, `JobsTable`, `CostReport`, `ProjectInfoRows` and so on) or reused from its package (`impact.PageData`). The page only draws what it is sent and keeps view state: the selection, the focused pane, scroll, and the SQL pane's vim cursor, which is the one piece of TUI behaviour ported to JavaScript (`assets/vim.js`, after `buffer.go` and `search.go`).

The page reads one stream of server-sent events (`/api/events`: the engine's state, a run's log and report, the editor's active file, a check's progress) and asks for the rest with JSON requests. Runs belong to the server, not to a tab: one at a time, as in the TUI; every open tab shows it, and a reload picks it up again. Each tab has its own selection and panes.

The server listens on 127.0.0.1 only, on a free port unless `--port` says otherwise. Every request must name a loopback host, must name this server in its `Origin` if it has one, and must have a `Sec-Fetch-Site` of `same-origin` or `none` if it has one (another site may only open the page itself, as a link does, never `/api/`, and not in a frame). A page can start runs that build tables, so another site in the same browser, or a DNS-rebinding name, must not reach it; browsers send these headers and pages can't set them, so they are enough for that. On loopback alone there is no token: it was turned into a cookie and dropped from the URL, and a cookie is shared by every port on 127.0.0.1, so a second xf web, a restart (a new token) or a browser that drops cookies left a reload with 401. A token only kept out other OS users and sandboxed processes on the same machine, which can read the project and the gcloud credentials anyway on a one-user Mac. With `--allow-host`, other devices can reach it, so the URL it prints carries a random token, which the first request turns into an `HttpOnly`, `SameSite=Strict` cookie, and every request needs it, a loopback one too. So does any request with a proxy's headers (`Forwarded`, `X-Forwarded-*`, `X-Real-Ip`), so a proxy that rewrites `Host` to 127.0.0.1 can't pass for this machine's browser. It runs in the foreground until ctrl+c. One `xf web` holds many projects, a tab showing one at a time (ADR 0008); ctrl+c in the projects list stops it.

The page is Preact and htm, vendored as ES modules with two small fonts and embedded with `go:embed`. There is no Node build: `go install` and the release keep working as they are, and `XF_WEB_ASSETS=<dir>` serves the files from disk while working on them.

## Considered options

- **The TUI in a browser terminal** (a PTY streamed to xterm.js). Parity on day one for little code, but it is a terminal in a tab: the browser's shortcuts fight its keys, and nothing can be clicked or laid out for a wide screen.
- **Logic in the page, from the compile graph.** Fewer endpoints and no round trip per keystroke, but two copies of every rule (palette ranking, lineage, run commands, report wording) that would drift. A search round trip on localhost takes about 10 ms on a 3,000-action project.
- **WebSockets.** One two-way connection, but a dependency and hand-written reconnects; server-sent events reconnect by themselves and the standard library serves them. (ADR 0011 later moved the stream to a WebSocket: each tab's event source held one of the browser's six connections to the server.)
- **React, TypeScript and Vite** (as in the VS Code webview spike). Typed and familiar, but either built files in git or a Node step in CI, the release and `go install`.
- **A daemon, or quitting with the last tab.** A daemon needs its own stop and status commands and a rule for orphaned runs; quitting with the last tab kills a run when a tab is reloaded or closed by mistake.

## Consequences

- `internal/web` holds the server and the page (`assets/`). It imports `internal/ui` for `Deps` and the shared helpers, so a change to how the TUI words or works something out reaches the web page too.
- Opening a file with `o` works when an editor is connected (through `xf lsp`) or the editor setting is a window editor; a terminal editor can't run from the browser, and the page says so. `Z` and `xf --nvim` have no web counterpart.
- The engine's wire protocol is unchanged: `xf web` is one more client.
- Later decisions take the page past the TUI on purpose: cmd+p for the palette (ADR 0005), on files (ADR 0014), a file tree with a source pane that edits any file in the project (ADR 0006), and `I` and `C` on a page of their own (ADR 0010).
- On 2026-10-19 the shared helpers and `Deps` moved from `internal/ui` to `internal/frontend`, which both front ends import; xf web no longer imports `internal/ui` (a test in `internal/web` checks). The TUI is in maintenance mode, and new features go to xf web alone.

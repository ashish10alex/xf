---
status: accepted
---

# xf web holds many projects at once, and a tab switches between them

One `xf web` server holds every project a tab has opened, not one at a time as ADR 0004 had it. The server keeps them by root, each with its own engine client, watchers, caches, run and stream of events; a project open in two tabs is open once, and both tabs share it.

A tab shows one project, which its URL names by the project's key: `/?project=finance`. The key is the folder's name or, when two remembered projects have the same name, the folder as the picker shows it (`~/work/a/shop`). Every request from the page names the key in the `X-Xf-Project` header, and its event stream names it in the query; the server's own events (the theme, the projects' summaries) reach every tab. The bare URL is the projects list. `xf web` started in a project opens its own `?project=` URL.

Projects open from the palette (`Project: switch or add`, in Scratch's palette too, where ⇧⌘P lists Scratch's own commands (amended 2026-10-25); its `⇄ projects` opens the list at once) or from the project's name in the status line, which shows how many others are open (`finance (+2)`) and lights up while one of them runs. The left column (the file tree's) also has a projects tab beside files, source control and search, which lists the open projects by name with what each is doing; a click opens one as the switcher does, and the column shows that tab in the project it opens. cmd+shift+w (ctrl+shift+w off a Mac) shows and hides the projects tab with the keys, as ctrl+g does Source Control, from anywhere in the project view, the editors included (amended 2026-11-05: it listed the projects in the palette before, which the help already called show/hide; the user wanted the tab). The palette's `View: open projects` lists the open projects in the palette (its `%` mode, with the box for a name or path): the cursor starts on the project this tab showed before, enter opens one as the switcher does and shift+enter the other way, and the column is left as it is (amended 2026-10-24). The switcher lists the open projects first, with what each is doing (compiling, running, its errors, unsaved files, its branch and tool), and then the remembered ones; typing a path opens a project xf has not opened yet. Its cursor starts on the project this tab showed before, so enter goes back and forth between two. A picked project opens in this tab or a new one as the user's `webOpenProjectIn` says (`same-tab` by default); shift+enter does the other. With two or more open, the palette's `Project: next open project`, which names the project it goes to, goes round them one at a time in the projects tab's order, back to the first after the last, always in this tab (amended 2026-10-22).

Switching in a tab is a snapshot and a reload, and so is reloading the tab. Before the tab leaves a project, it saves its workspace for that project in the tab's `sessionStorage`: the view it shows (the panes, Scratch, Source Control, the errors tab), the source pane's file, text, undo history, cursor, scroll and jump list, the selection, focus, zoom, lineage, columns, log tab and run flags, and the file tree's and Source Control's state. It then goes to the other project's URL. Coming back, the page boots as any page does and puts the snapshot back. With `sourceAutosaveMs` on, unsaved text is saved before the tab leaves; with it off, the text stays in the snapshot, and the page tells the server which files of the project are unsaved, so every tab's switcher marks it and closing it warns.

A project stays open, with its engine warm, until it is closed: `q` in it, or `x` or del on it in the switcher, which always asks first. Closing a project closes it for every tab, after saying which other windows show it; a local run of it is cancelled and a run in GCP carries on there. Each tab on it then goes to the project it showed before that is still open, or to the projects list. A tab's workspace for the project stays, unsaved text and all, and comes back when the project is opened again; the confirm says which files tabs have unsaved changes in. `xf web` still stops once no tab is open at all and no run is going (`webAutoExitSeconds`).

Global settings reach every tab; a project's settings, its Reopen banner and its Scratch belong to that project. Following the editor follows it in the editor's project.

## Why

Moving between projects meant `q`, which closed the project and took every tab back to the list: the engine stopped, and the dry runs, tables, compile and run log it had worked out were gone, as was everything the page showed. People work across a few projects in a day, and a second terminal with a second `xf web` was the only way to keep two open.

## Considered options

- **One project per server, as before, with a registry of every running `xf web`** (`~/.local/state/xf/web/<pid>.json`) to list and move between them. Each project stays isolated in its own process, but moving between them crosses origins, so a tab's state has to go through the server, and a server has to stay up for a tab that left it. It may still come later for `xf web`s started in separate terminals.
- **Swap the project in the page without a reload.** No reload, but every module of the page keeps state (`S`, `SRC`, `SC`, `G`, the tree, the editors), and each would need a copy per project with nothing leaking from one to another. A local reload is quick, and a snapshot taken through CodeMirror's `toJSON` keeps undo history across it.
- **Keep the snapshot on the server.** It would survive closing the tab, but two tabs on one project would share and overwrite one snapshot.
- **Close a project once no tab has shown it for a while.** Cheaper on memory, but the warm caches and run log are what switching is for; closing is one key.

## Consequences

- `internal/web` keeps a map of open projects (`projects.go`); a request that names no project gets the one open, when there is just one, so scripts and tests that know one project still work.
- Each open project holds an engine client and its watchers until it is closed: many open projects cost memory.
- A tab's workspace for a project lives in that tab: another tab, or the tab once closed, starts from the session's selection.
- ADR 0004's `q` (close the project, and every page back to the projects) is replaced by the rules here.

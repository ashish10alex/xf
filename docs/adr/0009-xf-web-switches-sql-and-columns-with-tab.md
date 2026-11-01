---
status: accepted
---

# xf web switches pane 2's sql and columns tabs with tab, not S

In xf web, `tab` goes to the next of pane 2's tabs while one of them has focus, and `shift+tab` to the one before: `sql` and `columns`, and `errors` first when the project has compile errors (errors → sql (stale) → columns, round again). It does nothing in the actions list, lineage, source or file tree. The log pane keeps its own `tab` round log, jobs and history (since ADR 0023, ⌥⌘←/→ instead, the terminal included). While you type in the columns filter, `tab` still swaps name and type; while you type a SQL search, the page leaves it alone.

`S` no longer does anything in xf web, from any pane. The palette's *View: columns* still switches from anywhere, and shows `tab` as its key. Pane 2's footers don't list `tab`; `?` does.

The TUI keeps `S`. This is the second key where xf web differs from the TUI on purpose, after cmd+p (ADR 0005).

## Why

The tabs are a strip at the top of the pane, and `tab` is how a browser, and the log pane beside them, goes round a strip. `S` is a letter you have to learn, and it only swapped two of the three tabs: on the errors tab it went to the SQL, and back to the errors only by `e` or a click.

xf web's panes are ctrl+0…3, so `tab` was free in pane 2. The TUI can't follow: there `tab` is the next pane (`keys.go`).

## Considered options

- **Keep `S` beside `tab`.** No habit broken, but two keys for one thing, and `S` still skips the errors tab.
- **Keep `S` only where `tab` can't reach** (the actions list and the log), to swap what pane 2 shows without moving focus there. Fewer keys to retire, but `S` would then work in some panes and not others.
- **`tab` from any pane.** It would take `tab` from the source editor's insert mode and the settings tabs, and do something to a pane that doesn't have focus.
- **Change the TUI too.** It would need a new key for the next pane.

## Consequences

- From the actions list, the columns of each action were ctrl+2, `tab`, ctrl+2 back, or the palette, where `S` was one key. Since ADR 0017, `c` switches the columns and the sql from any pane but the source and the file tree, leaving focus where it is, and the palette's *View: columns* shows `c`, not `tab`.
- A change to restore "the same keys as the TUI" must not bring `S` back here. ADR 0001's `S` describes the TUI.

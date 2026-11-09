---
status: accepted, cmd+p half superseded by ADR 0014
---

> ADR 0014 makes cmd+p find files, and `#` in it actions. What this one says of `/` still holds.

# xf web finds actions with cmd+p, and / searches the focused pane

In xf web, cmd+p on a Mac (ctrl+p on Linux and Windows) opens the palette to find an action, as quick open does in VS Code.
`/` only searches the focused pane, as it does in vim: the SQL (zoomed or not), the columns, and the source pane, where it starts the editor's own `/`.
In a pane with nothing to search (actions, lineage, run, log), `/` says which key finds an action.
The overlays that already searched with `/` (`,`, `?`, `T`, the projects list) keep it.

The key works from anywhere in the project view, the source editor included, as ctrl+shift+o does; not in Scratch, the settings page or the projects list.
Off a Mac, an editor or a text box keeps ctrl+p, which moves up in its completions or list there; outside them it no longer moves up, and `k` and `↑` still do.
On a Mac ctrl+p still moves up. cmd+p no longer prints the page.

The TUI keeps `/` for the palette. This is the first key where xf web differs from the TUI on purpose (ADR 0004 says the page has the same keys).

## Why

`/` meant two things: find another action, or search here.
Which one it did depended on the pane and on zoom.
In the SQL pane `/` searched only when zoomed, and in the columns pane only when it had fields; otherwise, and in the source pane outside the editor, it opened the palette. xf web has vim-keyed editors beside the panes, so `/` as search is what the hands expect, and a browser has a free, familiar key for go-to.

The TUI can't follow: a terminal never sends cmd to it, and ctrl+p is its Up key (`keys.go`), as in the lists of other terminal tools.

## Considered options

- **Keep `/` for the palette as a fallback** where no pane searches. Fewer habits broken, but `/` stays ambiguous and does something different in each pane.
- **Make the key a setting.** More to explain and test, for a preference no one has asked for.
- **Change the TUI too.** Parity, but the TUI would need ctrl+p (losing Up) or a less familiar key.
- **`/` jumps into the source editor's search whenever the source pane is shown.** Fast, but `/` would then ignore the SQL or columns pane that has focus.

## Consequences

- A change to restore "the same keys as the TUI" must not undo this. The help, the footer and the palette's command list show the key for the platform.
- The TUI's README section says `/` finds an action; the xf web section names the difference.

## Since 2026-10-20

Off a Mac the editors no longer keep ctrl+p: it opens the palette from them, as ⌘p does on a Mac, and is caught before vim, whose ctrl+p only moves up a line.
An editor keeps it while its completions show, where it moves up in them, and a text box still keeps it.

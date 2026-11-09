---
status: accepted
---

# xf web switches the columns and the sql with c, and compiles only with cmd+s

In xf web, `c` switches pane 2 between its `columns` and `sql` tabs.
From the `sql` or `errors` tab it goes to the columns; on the columns it goes back to the sql (`tab` and `shift+tab` still go round all the tabs, ADR 0009).
From the other panes inside pane 2 (the actions list, lineage, the run pane) and from the log, it does the same to what pane 2 shows and gives the `sql`/`columns` section the keys, so `z` zooms it (amended 2026-10-20; it first left focus where it was).
A pane zoomed with `z` unzooms so the columns can be seen.
With pane 2 hidden it says so, as `ctrl+2` does.
In the file tree it does nothing, as pane 2's other keys do, and in the source editor it is still vim's change.

`c` no longer compiles anywhere in the project view: cmd+s (ctrl+s off a Mac) does, from anywhere, saving the source pane first.
In the Run Changed overlay (`R`), where `c` compiled and asked again what changed, cmd+s now asks again too.
The dry run errors overlay loses its `c`.
The I/C checks page keeps `c` for "check again", which compiles only when the check failed on a compile error; it is a check, not a compile.

The footers list `c` nowhere, as they don't list `tab`; the errors tab and the errors overlay list cmd+s for compile.
`?` lists `c` with pane 2's tabs, and the palette's *View: columns* shows it.

The TUI keeps `c` for compile. This is the third key where xf web differs from the TUI on purpose, after cmd+p (ADR 0005, 0014) and `tab` (ADR 0009).

## Why

Two keys compiled, and cmd+s is the one hands already know from every editor, the source pane's included.
`c` was the shorter of the two, but the columns are what pane 2 is asked for more often, and since `S` went (ADR 0009) they took ctrl+2, `tab`, ctrl+2 from the actions list.
`c` is the letter for columns, and leaving focus where it is makes the actions list a way to browse every action's columns.

## Considered options

- **`c` shows the columns only while pane 2 has focus**, and compiles from the other panes.
  Fewer changes, but one key would mean two things by where the focus is.
- **`c` stays on the columns when they already show**, so pressing it twice never leaves them, and `tab` goes back.
  This was the first choice; after trying it, a switch, one key both ways, was the better fit.
- **`c` takes focus to the columns**, as ctrl+2 then `tab` would. Then `j`/`k` move through the columns, not the actions, and browsing needs ctrl+2 each time.
- **Keep `c` compiling in the R overlay and the errors overlay**, where no columns show. Then `c` would still compile in some places.
- **Change the TUI too.** It is in maintenance mode, and its `c` and `S` stay.

## Consequences

- "Press c to retry" and the watch-off warning say cmd+s.
- A change to restore "the same keys as the TUI" must not bring `c`'s compile back here.

## Amended 2026-10-20: `c` takes focus

`c` now gives the `sql`/`columns` section the keys, from wherever in pane 2 or the log it is pressed, so a `z` after it zooms that section, as the user expects of a key that switches a section.
This is the option above that was turned down: browsing every action's columns with `j`/`k` in the actions list goes, and `ctrl+2` goes back to the list.

## Amended 2026-10-23: `c` goes on to the lineage

`c` now goes round three tabs, `sql`, `columns`, `lineage` and back to `sql`, because switching between two left the lineage to `O` or `tab`.
From the `errors` tab it still goes to the columns.
It gives the lineage the keys as it does the other two.

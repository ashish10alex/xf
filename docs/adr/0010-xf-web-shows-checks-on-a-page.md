---
status: accepted
---

# xf web shows the column impact and query cost on a page, not an overlay

In xf web, `I` and `C` open the Checks page, which takes the whole window as the settings page does: the project view goes, and comes back as it was on `esc` or *× close*. The page has a tab for each check, *Column impact* and *Query cost*; `tab` goes round them, and `I` and `C` go straight to theirs. Its top bar has buttons for what the overlay's keys did, each showing its key: *Working tree / Pushed branch* (`a`), *Copy Markdown* (`y`) and *Check again* (`c`). `j/k`, `ctrl+d/u` and `g/G` scroll it, and `e` on a compile error goes to the errors tab, as before.

The report is the one the overlay showed. Its tables, verdict, notes and legend take the page's width, and a name wraps at its underscores and dots only on a phone. (They first wrapped at about 110 characters, which broke short lines on a wide screen for no gain.) On a phone, a table scrolls sideways in its own box.

While the page shows, it has only its own keys: the palette, `?`, `r` and the panes' keys wait until you leave it. ctrl+, still opens settings, and closing them comes back to the page. The page adds nothing to the browser's history, as settings and Scratch don't: the back button doesn't close it. The first check of a session can still stop to ask for the prod compiler options, in the same prompt, over the page.

A check is no longer started again each time you look. Leaving the page leaves a check running, and coming back shows it running, failed or done. A done report is checked again by itself, afresh, when the branch has moved on since: on coming back to it, and while the page shows it, after a compile (it shows dimmed meanwhile). Otherwise only `c` checks again.

The TUI keeps its `I` and `C` overlays.

## Why

The impact report is a wide table (the model, what changed and who is affected) with a second table of readers under it. In an overlay capped at 1080px by 78% of the window, the columns wrapped into tall rows and the report scrolled inside a box, on a screen with room to spare. On a page the tables have the whole width, and the report reads as a document.

The two checks shared one overlay, one header and one set of keys, so they move together: one page with two tabs, rather than a page for one and an overlay for the other.

Checking again on every look was cheap to write but cost a round of dry runs each time you switched between the checks, and threw away a running check's progress if you closed and reopened it.

## Considered options

- **A bigger overlay**, as the errors overlay zooms to 96% of the window. The smallest change, but still a floating box with a margin and a dimmed project behind it, over a report that has nothing to do with the panes under it.
- **A page each for I and C.** No tabs, but switching meant learning the other key on the page.
- **Keys only, in a footer**, as the overlay had. Less to draw, but a full page with nothing to click reads as unfinished, and settings, the page this one follows, shows its controls.
- **Links from the report to the actions it names.** Useful, but a new feature with its own questions (which pane, what happens to the page); this change is about room.
- **Change the TUI too.** A terminal is already the whole window; its overlay fills most of it.

## Consequences

- `S.checks` is the page's tab, or null; `S.overlay` no longer holds `impact` or `cost`. Keys that work "anywhere in the project view" (ctrl+g, cmd+b, ⌥⌘b, ctrl+0…3, cmd+j, ctrl+shift+o) check `S.checks` too.
- A report is per project: opening another project forgets both.
- This is the third place xf web leaves the TUI's shape on purpose, after cmd+p (ADR 0005) and `tab` in pane 2 (ADR 0009).

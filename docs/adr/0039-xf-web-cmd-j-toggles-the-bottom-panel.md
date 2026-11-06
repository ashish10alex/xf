---
status: accepted
---

# xf web's cmd+j toggles the bottom panel; cmd+` is the terminal

`⌘j` (`ctrl+j` off a Mac) shows and hides the log pane, xf web's bottom panel, as VS Code's Toggle Panel does: hidden, it shows again on the tab it showed last (log, jobs, history, preview or the terminal) and gives it the keys; showing, it hides, and the keys go back to the source editor. `⌘\`` (`ctrl+\``; on a Mac `ctrl+\`` too, VS Code's own key there) is the terminal: it shows the terminal tab with the keys, and hides the panel when the terminal already has them. Both work from inside the editors and the terminal. The palette has *View: bottom panel*, and the rail's terminal icon does what `⌘\`` does. A reload keeps the terminal as the last tab only when the panel was hidden, so `⌘j` brings it back, and a page never opens on a new shell. This changes ADR 0023 and 0035, where `⌘j` was the terminal.

## Why

The user asked for it. With a terminal tab, `⌘j` always went to the terminal, so from the jobs or the history tab, hiding the panel and showing it again lost the tab. Opening the terminal is a separate wish, and VS Code gives it its own key.

## Considered options

- **Showing a shown panel's keys before hiding it,** as `⌘j` did for the terminal. VS Code's Toggle Panel hides it, and a toggle that sometimes only moves the keys is harder to predict.

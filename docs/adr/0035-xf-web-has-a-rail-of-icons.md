---
status: accepted
---

# xf web has a rail of icons

A column of icons, 28px wide, sits on the far left of the project view, left of the file tree's column: files, Search, Source Control, the open projects, the terminal, and the settings' gear at its foot.
Each icon does what its key does (`⌘b`, `⇧⌘f`, `ctrl+g`, the projects tab, `⌘j`, `ctrl+,`) and its title names the key, so the mouse finds each place and the hover teaches the key.
The icons are muted until the mouse comes near; the one showing is lit with a bar on its left, and a click on it again hides it, as its key does.
Source Control's icon counts the changed files, the projects' icon the open projects when more than one.
A zoomed pane hides the rail, as it hides the tree.
The gear that sat on the first pane's border, top right, moved to the rail's foot; the list of projects keeps its own.

The column's header lost its tabs (files, source control, search, projects) and its ✕, which the rail does: it is a title like every pane's, `0` and the view it shows, with that view's count (changed files, matches, open projects) and the tree's notes (not git, first 20000).

## Why

Nothing on the page showed that the file tree, Search, Source Control, the other projects or the terminal were there: a new user, most of all one from VS Code who doesn't use vim's keys, had to know a key or find it in the palette.
The user picked the rail from three served mockups.

## Considered options

- **Icons at the start of the status line.** No new column, but at the bottom, away from where the views open on the left, and in a line that is full already.
- **Folded panes.**
  A closed column and log pane keep their hairline with their titles set in it (files · search · git down the left edge, log · jobs · history · terminal along the bottom): words, as every pane title is, but harder to find than icons.
- **Hints that show once.** They help on the first day, then they are gone.

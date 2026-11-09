---
status: accepted
---

# xf web shows the open file with cmd+shift+e

`⇧⌘e` (`ctrl+shift+e` off a Mac) is VS Code's Show Explorer in xf web: from anywhere in the project view, the editors included, it shows the file tree with the open file's row in the middle (its folders opened) and gives the tree the keys, swapping out Source Control, Search or the projects when the column showed one.
Pressed while the tree has the keys, it gives them back to the source editor and leaves the tree showing, as VS Code's toggles focus back to the editor without closing the sidebar.
The palette has it as *View: show the open file in the file tree*, and `?` lists it.
The terminal keeps the key for the shell.

## Why

The user asked for VS Code's key.
`⌘b` already reveals the open file when it shows the tree, but it hides the tree when the tree shows, so from a tree that shows another file it takes two presses, and it never goes back to the editor without hiding the column.

## Considered options

- **Revealing without giving the tree the keys.**
  VS Code's explorer.autoReveal does that as the editor changes file; xf's tree already follows the open file's row when it shows, so the key's job is the focus.
- **Hiding the tree on the second press,** as `⌘b` does. Then the two keys would do the same thing.

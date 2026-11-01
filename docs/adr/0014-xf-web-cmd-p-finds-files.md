---
status: accepted
---

# xf web's cmd+p finds files, and # in it finds actions

In xf web, cmd+p on a Mac (ctrl+p on Linux and Windows) opens the palette on the project's files, as VS Code's quick open does. Typing `#` in it finds an action, as the palette did before; a query that starts with a `$kind` finds actions too, so `$view orders` still works. `>` lists commands and `@` the symbols of the file shown, as before. No other key finds an action: it is cmd+p, then `#`. This replaces the first half of ADR 0005; its second half, `/` searching the focused pane, stands.

The files are the file tree's (ADR 0006): what git lists, untracked files that aren't ignored included, or a walk of the project when git can't list them. A file has a row of its own, with the tree's marks: its git letter, how many actions it has and its errors. The query matches the path only, those with every word in the file's name first, then in its path, then with the letters scattered. With nothing typed, the file shown leads, then the source pane's buffers (ADR 0012), the last shown first, then the files git has changed, then the rest by path. The cursor starts on the second row, so enter goes back to the file before, and cmd+p again moves down, as before. Picking a file opens it as the tree does: its first action selected, or the file alone when it has none. The palette lists files before the first compile, when it has no actions.

The TUI keeps `/` for the palette, on actions.

## Why

The source pane edits any file in the project (ADR 0006), so the palette's key is how the hands go to a file, as in an editor. Actions alone left files that define none (`workflow_settings.yaml`, `package.json`, includes, the README) out of reach of the keyboard, but for the tree. A JS file with many `declare()`s is one file, not one of many actions.

## Considered options

- **Files and actions in one list.** No mode to think about, but a `.sqlx` shows twice (the file and its action), and ranking two kinds together is guesswork.
- **Actions nested under their files.** Richer, but a long list for j/k to walk.
- **A key of its own for actions**, beside `#`. A browser tab never gives a page cmd+t, VS Code's key for it, and the action finder is one character away in the box.
- **Match a file on its actions' names too**, so `raw.orders` finds `definitions/sources.js`. That blurs files and actions again; `#` is for that.
- **`path:42` to open at a line.** Left for later; errors and `gd` already land on lines.

## Consequences

- The help, the empty panes' hint and `/`'s hint say "cmd+p then #" for an action. The commands list has "Go to: file" and "Go to: another action".
- The palette asks for `/api/files` each time it opens on files, and shows the last list while it waits; enter before the first list comes picks once it has.

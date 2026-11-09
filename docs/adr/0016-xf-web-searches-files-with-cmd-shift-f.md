---
status: accepted
---

# xf web searches the project's files with cmd+shift+f, and replaces in them

In xf web, cmd+shift+f on a Mac (ctrl+shift+f there too, and ctrl+shift+f on Linux and Windows) shows a Search view in the far-left column, beside the file tree (ADR 0006) and Source Control (ADR 0007), as VS Code's search view is.
The key works from anywhere in the project view, the source editor included, where it is caught before vim, as cmd+p is (ADR 0014); not in Scratch, the settings page or the projects list.
It always shows the view with the box focused, on the source editor's selection when that is on one line, else on the last query, selected. cmd+b hides the column while Search shows, and ctrl+g swaps it to Source Control.
On a phone the view covers the page, as the tree does, and a tap on a match closes it.

- **What it searches.**
  The text of the files the tree lists: what git lists, untracked files that aren't ignored included, or a walk of the project without git.
  Binary files and files over 2 MB are skipped, as the source pane refuses them.
  The file the source pane shows with unsaved changes is searched as typed there.
- **How it matches.**
  The server matches, in Go, and a replace uses the same code, so it changes what the list showed.
  Case is ignored by default; alt+c matches case, alt+w whole words, alt+r a regular expression (RE2: `^` and `$` are a line's ends, `$1` a group in the replacement).
  A `…` shows fields of globs to include and exclude, separated by commas: one without a `/` matches a file or folder by name anywhere (`*.md`, `includes/`), one with a `/` from the project's root (`definitions/**`).
- **When.** About 200 ms after typing stops; an answer to an older query is dropped. A file changed on disk searches again, keeping the cursor.
- **Limits.** 5,000 matches, then "5,000+ results, narrow the search", and replace all refuses.
  A match's line is cut to a few words before it and a line after.
- **Keys.**
  In the box, enter or ↓ goes to the list, esc back to the source editor.
  In the list, j/k move and show the match in the source pane without the keys leaving the list or the jump list growing; enter goes there, the editor in vim's normal mode at the match; h/l close and open a file's matches; x dismisses a match or a file; r replaces the match or the file's; `/` or esc goes back to the box.
  F4 and shift+F4 go to the next and previous match from anywhere.
- **Replacing.**
  A `▸` beside the box shows the replace field.
  Each match then shows its text struck through beside what it becomes, which the server works out.
  A match, a file and the whole list each have a replace; replace all (cmd+alt+enter, ctrl+alt+enter) asks first, with how many matches in how many files.
- **The file the source pane shows** changes in its editor, in one change that `u` undoes and autosave (or `:w`) writes.
  A match whose text isn't where the search found it is skipped there.
- **Other files** change on disk.
  Each replace sends what it expects of each match: the file's hash, its line's text and where on the line.
  In a file unchanged since the search, a match is replaced where it was; in a changed one, on the nearest line with the same text; else it is skipped, and the message says how many were.
  A file keeps its line ends and byte-order mark.
- **Undo.**
  "Undo replace" (a button, and a command in the palette) puts back the files the last replace wrote, but for those that changed since, which it names.
  One level.
  The engine's watcher compiles what a replace or an undo writes.
- **Kept.**
  The query, the replacement, the toggles and the globs, per project, in the browser's storage; a reload searches again.
  Dismissed matches are not kept.

The TUI has no Search.

## Why

The palette (ADR 0014) finds a file by its path and an action by its name, and `/` searches the pane that has focus (ADR 0005).
Nothing found a name, a column or a macro in the files' text, and renaming one by hand across files was the usual reason to leave xf for an editor.

## Considered options

- **A mode of the palette** (a prefix as `#` is).
  Little to draw, but the palette closes on a pick, and walking through matches means opening it again for each one.
- **A tab of pane 2.** Close to the editor, but it hides the compiled SQL, which a search across files is often checked against.
- **Search the compiled SQL too.** It would find where a column ends up, but a match there has no line in a file to go to, and needs a compile.
- **git grep to find, Go to replace.**
  Faster on a very large repository, but the two read regular expressions differently, so a replace could change what the list didn't show, and a project outside git needs a second path anyway.
- **On enter, not as typed.** Predictable, but slow to use, and a Dataform or dbt project is small enough to search per key.
- **Rely on git to undo.** No undo of its own, but discarding a file in Source Control loses edits made before the replace.
- **The key toggles the view.** As ctrl+g does; but pressed in the editor to search for something else, it would hide the view. cmd+b hides it.

## Consequences

- The column's header has three tabs; those not shown shrink first in a narrow column.
- The page's first load fetches two more modules (`search.js`, `search-view.js`); `TestFirstLoadBudget` allows 45.
- The help, the README and the palette (`Search: in files`, `Search: undo the last replace`) show the keys.

---
status: accepted
---

# xf web has a file tree, and its source pane edits any file in the project

xf web gets a file tree in a column on the far left, as VS Code's explorer is. cmd+b on a Mac (ctrl+b on Linux and Windows) shows and hides it, as in VS Code and as in the Scratch view, whose list the same key toggles; it starts hidden, and whether it shows, its width and its open folders are kept in the browser, per project. ctrl+0 gives it the keys, beside ctrl+1 (source) and ctrl+2 (the right-hand pane).
The keys are nvim-tree's: `j`/`k` move, `l` or enter opens a file or a folder, `h` closes a folder or goes to its parent, `a` makes a file (a name ending in `/` makes a folder), `r` renames or moves, `m` moves (the same box, with the folder selected), `d` deletes, `y` and `Y` copy the absolute and relative path, `/` filters.
A click on a file opens it and the tree keeps the keys, as VS Code's explorer does; a double-click gives them to the editor.
A right-click shows a menu of the same actions (New File… on a folder, Copy Path, Copy Relative Path, Rename…, Move…, Delete).
It can show while the source pane is hidden; opening a file shows the pane.
On a phone it covers the page and closes once a file is picked.

It lists what git would: the files git tracks and the ones it doesn't yet, without the ones it ignores (`git ls-files --cached --others --exclude-standard`), and empty folders.
Outside a repository it walks the project, leaving out `.git`, `node_modules`, `target`, `dbt_packages` and `logs`.
The server watches the project for files made or removed outside the page (a shell, a git checkout, another editor) and tells the page, which lists them again.
A file is marked with its git status, with ● while it has unsaved changes and in red when the compile has errors in it; a folder takes its files' git and error marks.

The source pane now edits any text file in the project, not only an action's file.
An include, a macro, `workflow_settings.yaml` or a dbt `schema.yml` is edited as an action's file is (vim, `:w`, the conflict on save, autosave, git's hunks), with colours for the languages CodeMirror's legacy modes have (JavaScript, JSON, YAML, TOML, Python, shell) and plain text for the rest, but without what needs an action (ref completion, K, gd, the dry run's marks).
Files that aren't UTF-8 text, or are larger than the pane edits, are not shown.
While such a file is open, no action is selected: the actions pane keeps its list with no row picked, the lineage, run and SQL panes show the file's path and say it has no action, and picking an action shows its file again.
One file is open at a time; ctrl+o and ctrl+i walk back and forward through the files as before, and ctrl+^ goes back to the file before.
Following the editor follows it into any file.

From the tree a file or folder can be made, renamed or moved, and deleted, which moves it to the system's trash (the Trash on macOS, the Recycle Bin on Windows, the freedesktop trash on Linux), never deleting it outright.
Renaming an action's file renames the action when the action is named after its file, as Dataform does without `config { name }` and dbt always does; the confirmation then says which actions are renamed and how many actions in how many files refer to them, and their refs are left as they are.
The file with unsaved changes can't be renamed or deleted.

The TUI has no file tree: it has no editor of its own, and leaves to nvim or `$EDITOR` for that.

## Why

The source pane made xf web an editor for a project's actions, but not for the project.
The files that shape every action (includes, macros, project settings, schema files) still needed another editor, and so did a new action's file.
A file tree is how editors reach every file, and `cmd+b` is the key people know for it.

## Considered options

- **Open only action files from the tree** (the tree as another way to pick actions).
  Nothing changes for the panes on the right, but includes and settings still can't be edited.
- **Replace the actions pane with the tree** (folders, files, and the actions in each). One list instead of two, but it moves a pane the TUI has and its keys.
- **Tabs in the source pane.** More like VS Code, but each tab keeps its own unsaved text, autosave and conflicts, and the layout is dense already.
- **Rewrite refs when a rename renames an action.**
  What an IDE would do, but it is a refactoring feature with its own preview and failure cases; the compile's errors list what to fix.
- **Delete outright, or to a folder of xf's own.** One code path, but a deleted untracked file is gone, or kept where nobody looks.
- **Refresh the tree only on a compile, a focus or a key.** No watcher, but the tree is stale after a `git checkout` until one of those.

## Consequences

- xf web goes past the TUI's panes here, beyond ADR 0004's parity: a change for parity must not remove the tree.
- The server's source endpoints accept any regular file inside the project; they still refuse paths outside it, through `..` or symlinks, and `.git`.
- `internal/trash` moves a file to the system's trash on each platform: on macOS by moving it into `~/.Trash` (without Finder's "Put Back"), so no permission to control Finder is asked; on Windows with `SHFileOperationW`; on Linux after the freedesktop trash specification.

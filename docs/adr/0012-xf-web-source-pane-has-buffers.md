---
status: accepted
---

# xf web's source pane has buffers, with a leader key

The source pane keeps a list of buffers, as vim does: every file it has shown, in the order it first showed them, each with a number it keeps while it is listed.
A removed buffer leaves a gap, so `:b 3` always means the same file.
`<leader>]` and `<leader>[` (`:bn`, `:bp`) go to the next and previous one, wrapping at the ends.
`<leader>b` (`:ls`, `:buffers`) opens a list to type into and pick from, with `%` on the file shown and `#` on the alternate one.
`:b N` or `:b name` goes to one, and `:bd`, or `del` in the list, removes one; for the file shown, the pane goes to the alternate buffer first, else the one before it.

xf web gets a leader key, `vimLeader`, `<Space>` by default, which `<leader>` stands for in the vim keys.
The three buffer commands are vim keys like the others (`vimBufferPrev`, `vimBufferNext`, `vimBuffers`), so any one of them can be set to plain keys.

Added 2026-10-23: `<leader>x` (`vimBufferDelete`) is `:bd` on the buffer shown, and `<leader>f` (`vimFiles`) opens the palette's file finder, as `cmd+p` does.
The user had both in neovim.

- Going to a buffer with unsaved changes saves them first, autosave on or off.
  A failed save (the file changed on disk, a write error) keeps the pane where it is, with the conflict a `:w` would show.
- It selects the action the buffer last showed, so pane 2 follows. A file with no action opens on its own.
- It adds nothing to the `ctrl+o` jump list, and it makes the file left the alternate one, for `ctrl+^`.
- A buffer whose file is gone is dropped when a step or the list reaches it, and a step goes on to the next.
- The list is the project's, kept by the server while the project is open, so a reload and the project's other tabs have it.
  As vim's, it starts empty each time xf opens the project; it is not kept in the session file.
  (It was at first, but a list that grew over weeks put files opened one after the other far apart, so `<leader>[` from the file just opened went to some old file, not the one before it.)

## Why

The user moves between files the way they do in neovim, with `<leader>[` and `<leader>]` bound to `:bp!` and `:bn!`.
`ctrl+^` only knows one file back, and the palette finds actions, not the files you have open.

## Considered options

- **The recent actions list (`S.recent`) as the buffers.**
  It is already kept, but it is ordered by last use, so stepping through it would bounce between the same two files.
- **Keep unsaved changes in hidden buffers**, as `:bn!` does.
  This is closest to vim, but every place that checks for unsaved changes (leaving the page, closing the project, the unsaved marks) would have to know about files that are not shown.
- **Keep the list in the session file**, as it first was.
  A reload of xf has the files of last time, but the list only grows, so the order no longer says what was opened just before.
- **A `b:` mode of the cmd+p palette** for the list. It would share the palette's code, but the palette ranks actions, not open files.
- **Space as a fixed leader.** No new setting, but no way to change it.

## Consequences

- CodeMirror's vim runs a key's own command as soon as it is pressed, even when longer keys start with that key.
  So while a command uses the leader, the leader loses its own mapping in normal mode: Space no longer moves right there (it still does in visual mode), and Space followed by a key nothing is mapped to does nothing.
  This applies in the Scratch editor too.
  A leader other than Space gets its own mapping back only on a reload.
- `config.VimClash` checks the keys with `<leader>` written out, so a leader that makes two commands the same (`g` and `<leader>d` against `gd`) is refused.

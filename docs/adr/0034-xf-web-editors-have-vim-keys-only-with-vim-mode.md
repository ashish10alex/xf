---
status: accepted
---

# xf web's editors have vim's keys only with vimMode

xf web's CodeMirror editors (the source pane, Scratch's editor and its run-SQL tab, the commit message box, the settings' JSON, a difference) take vim's keys only when `vimMode` is `true`.
It is off by default: they then have CodeMirror's own keys, as VS Code's.
`vimMode` is a user-only config key with no flag, a toggle at the top of Settings' Vim tab, and it applies at once in every open tab: vim's handler and the default keymap each sit in a compartment of every editor, which the settings event reconfigures.
The SQL pane's own vim cursor (`vim.js`) and the panes' `j`/`k` are xf's keys, not an editing mode, and stay.

Without vim:

- Closing an overlay or a page gives the keys back to the source editor ready to type, where with vim they come back in normal mode.
- `esc` closes one thing open (completions, a card, the find panel, extra cursors or a selection) and, with nothing open, does nothing, as VS Code's: the editor keeps the keys.
  `ctrl+w` leaves the editor, and ctrl+q a page or the project (ADR 0030).
  (Amended 2026-11-05: esc with nothing open left the editor, as `ctrl+w` does, until the user found it took the keys away where VS Code's esc doesn't.)
- Autosave's countdown starts at each change and on leaving the editor; with vim it waits for the end of insert mode.
- ctrl+o and ctrl+i walk the source pane's jump list, bound ahead of CodeMirror's split line; `/` in pane 1 opens the find panel.
- What vim's keys and ex commands do is in the palette's `>` commands (next and previous git change and error, go to the file named under the cursor, the file before, the buffers, discard unsaved changes) and the right-click menu (next and previous change and error).
  No new default keys.
- The messages name each mode's way: `:w` and `:e!` with vim, ⌘s and the palette's "discard unsaved changes" without.
  One module with no imports (`vim-mode.js`) words them, so the messages need not load the editor.

With vim on, each editor is as it was: the same extensions in the same order, the vim key list, ex commands and messages unchanged.

## Why

The user wanted a setting to turn vim off without breaking the vim path.
The default is off, so a new user meets the keys most editors have; a vim user sets `vimMode` once in the user config.

## Considered options

- **Vim on by default.** No change for anyone upgrading, but a new user meets modal editing first.
- **Applying on reload.** Simpler, with no switch mid-session, but the other editor settings (wrapLines, the vim keys) apply at once.
- **VS Code's keys for vim's commands** (F8, ⌥F5, F12).
  Each needs a clash check with the browser, the app window and Windows; the palette and the menu reach them now, and keys can follow.
- **Turning off the SQL pane's vim cursor too.** It is xf's read-only pane, as the TUI's, not an editor.

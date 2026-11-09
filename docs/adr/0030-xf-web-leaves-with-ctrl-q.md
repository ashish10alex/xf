---
status: accepted
---

# xf web leaves with ctrl+q, not esc then y

In xf web, ctrl+q (ctrl on a Mac too, where cmd+q is the browser's) goes back one level from anywhere, the editors included: an open overlay or the theme picker closes; else the Settings, Scratch or Checks page goes back to the project; else the project closes.
It acts at once.
Only a project that would lose something asks first: a run in progress, another tab's unsaved changes to its files, or a command running in the terminal tab.
That confirm takes `y`, `enter` or ctrl+q again, and any other key stays.

esc no longer leaves anything, and the page no longer asks "back to the project? y". It still closes what it can (zoom, visual mode, filters, overlays).

## Why

The 2026-10-20 exit asked on esc and confirmed with `y`.
Both are vim's keys: esc is what a vim user presses to be sure of normal mode, so a second esc opened the question, and `y` is the yank operator, so the start of `yy` after it answered yes.
A chord a vim user doesn't type by accident has neither problem, and it needs no question to guard it.

## Considered options

- **`:q` / `ZZ`.** Familiar, but it works only in an editor, and the Checks page and Scratch's results have none.
- **`<leader>q`.** The most vim-like, but three keystrokes, and again only where an editor has the keys.

## Consequences

- ctrl+q is caught before vim, so vim's `ctrl+q` (block-wise visual, an alias of `ctrl+v`) is not available in xf's editors; `ctrl+v` still is.
  The terminal tab keeps ctrl+q for the shell and for vim inside it.
- Firefox on Linux uses ctrl+q to quit and may not hand the key to the page. An `--app` window (ADR 0013) has no such binding in Chrome, Edge or Chromium.
- The projects page has nothing to leave; ctrl+c there still stops xf web. `q` and ctrl+c leave nothing, as before.

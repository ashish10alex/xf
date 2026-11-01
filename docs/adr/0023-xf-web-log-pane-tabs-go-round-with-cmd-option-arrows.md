---
status: accepted
---

# xf web's log pane goes round its tabs with ⌥⌘← and ⌥⌘→

In xf web, ⌥⌘→ shows the log pane's next tab and ⌥⌘← the one before (ctrl+alt+→ and ctrl+alt+← off a Mac), while the log pane has the keys: log → jobs → history → terminal, and round again. Without kept runs it is log ↔ terminal. The key works from inside the terminal: xterm leaves it to xf, as it does ctrl+0…3, ⌘j and ⌘z. Going to the terminal gives it the keys; leaving it gives them back to the log pane. `tab` and `shift+tab` no longer go round the log pane's tabs; `t` still does, and now reaches the terminal too (from the terminal, `t` is the shell's). Pane 2 keeps `tab` (ADR 0009).

## Why

The terminal tab needs `tab` for the shell's completion, so `tab` could never leave it, and the terminal was left out of the round: from it, only a click, ⌘j or L reached the log again. ⌥⌘←/→ is what a Mac's terminals and VS Code use to go to the next and previous tab, and a shell has no use for it.

## Considered options

- **Keep `tab` and add ⌥⌘←/→ for the terminal only.** Two keys for one thing, and `tab` would go round in some tabs and not others.
- **ctrl+tab.** Browsers keep it for their own tabs, and `preventDefault` can't take it back.

## Consequences

- In an ordinary Chrome tab on a Mac, Chrome uses ⌥⌘←/→ to switch its own tabs and may not hand the key to the page; an app window (`--app`, ADR 0013) has no tabs, so the key reaches xf there.
- The footers and `?` show ⌥⌘→ (ctrl+alt+→) for the log pane's next tab, the terminal's footer included.

---
status: accepted
---

# xf web can open in an app window, and the palette key again moves down

Off a Mac, a browser tab never gets ctrl+n: Chrome, Edge and Firefox keep ctrl+n (new window), ctrl+t and ctrl+w for themselves, and `preventDefault` can't take them back. So xf web's ctrl+n, the TUI's Down, never moved on Linux or Windows. ctrl+p does reach the page, because a page may take the print key, so in the palette ctrl+p moved up while ctrl+n opened a browser window.

There are two changes:

- **The palette's key, pressed again with the palette open, moves down its list**: ctrl+p off a Mac, ⌘p on a Mac, as VS Code's quick open does. The rule is the same on every platform and in every kind of window. ↑ moves up. Elsewhere, ctrl+n and ctrl+p are as before.
- **`webBrowser` (a user setting, `--web-browser`) can open the page in an app window**: a Chromium window for the one page, opened with `--app=URL`. `app` takes the first Chrome, Edge or Chromium found, and a path or name picks the browser. `xf web --app` is `app` for one run, and the default, `default`, is the system's browser as before. The app window uses the normal browser profile, so sign-ins and xf web's per-browser state carry over. It works the same on a Mac, where it launches with `open -na <app> --args --app=`. In WSL, where xf is a Linux program but the browsers are Windows's, it looks for them on the C: drive (`/mnt/c/Program Files…` and each user's `AppData\Local`) before a Linux one, and runs the `.exe` through WSL's interop. When that interop is missing (its `WSLInterop` binfmt_misc rule is gone, as when something clears binfmt_misc while WSL runs, or `/etc/wsl.conf` turns interop off), Linux refuses the `.exe` with "exec format error", so xf web skips Windows's browsers and prints how to bring the rule back: restart WSL, which adds it again, or register it by hand. WSL forwards Windows's 127.0.0.1 to WSL, so the window reaches xf web. When no browser is found, or the one found won't start, xf web says so and opens a tab.

An app window gets ctrl+n, ctrl+t and ctrl+w. Off a Mac, xf takes ctrl+n everywhere, so a press where nothing moves does nothing, rather than open a blank browser window. ctrl+t and ctrl+w stay the browser's, and so do ⌘n, ⌘t and ⌘w on a Mac. The page knows it is in one by `display-mode: standalone`, and the help lists ctrl+n/ctrl+p among the move keys only where they move (a Mac, or an app window).

## Why

The TUI's ctrl+n/ctrl+p are muscle memory in the lists. A tab can't have ctrl+n off a Mac, so the page needs either a window that can, or a key that reaches it. The palette is where the pair is used most, and VS Code users already press ctrl+p again to go down there.

A check on Windows 11 with Edge 155 showed that an `--app` window receives ctrl+p, ctrl+n, ctrl+t and ctrl+w as keydowns, that `preventDefault` holds, and that no window opens.

## Considered options

- **Arrows only off a Mac.** Nothing to build, but ctrl+n stays dead on Linux and Windows.
- **An installable PWA (a manifest).** An install belongs to one origin, and xf web listens on a free port each run unless `--port` fixes it, so the install would break. It would also be launched from the Start menu, not from `xf web`.
- **Another down/up pair off a Mac.** There is hardly a free ctrl+letter: ctrl+j shows the log, and most of the others belong to vim or the browser.
- **The app window by default.** ctrl+n would just work, but a Firefox user would get an Edge window they didn't choose.
- **ctrl+p up, paired with ctrl+n, in an app window.** That is the TUI's pair, but the same key would then move a different way depending on the window.

## Consequences

- The palette's ctrl+p no longer moves up. ADR 0005 still holds: outside the palette, an editor or a box keeps ctrl+p.
- `xf web` opens a browser process of its own when `webBrowser` asks for one. It is started detached, so ctrl+c in the terminal doesn't reach it.
- Firefox has no app window, so a Firefox user off a Mac moves with the arrows.

## Since 2026-10-20

In an app window off a Mac, ctrl+p in the palette moves up and ctrl+n down, as in the TUI: the alternative rejected above. On Windows the user pressed ctrl+p for up in the command palette and got down, where a Mac's ⌘p and ctrl+p are two keys and never clash. In a tab off a Mac ctrl+p still moves down, since ctrl+n never reaches it there; a Mac is unchanged.

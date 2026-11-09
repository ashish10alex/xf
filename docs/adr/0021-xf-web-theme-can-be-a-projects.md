---
status: accepted
---

# xf web's colour theme can be a project's

`webTheme`, `webThemeLight` and `webThemeDark` can be set in a project's `.xf/config.json` as well as in the user's config file.
The user's file stays the setting for every project; a project's file wins over it, key by key, for that project only.
The TUI's `theme` is still the user's alone.

## Picking

The `◐` picker, in a project, saves for every project (the user's file, as before) or for this project alone (its `.xf/config.json`); `tab` switches between them.
It opens on "this project" when the project's file already sets a theme, so a pick in such a project doesn't seem to do nothing.
Outside a project it saves to the user's file.

A project that has its own theme shows `· project` on the button.
The picker lists an **inherit** row there, which takes all three keys out of the project's file so the user's theme applies again.
The Settings page edits the same keys at either level.

## Telling the pages

A change to the user's theme reaches the projects' list as before, and each open project's pages get that project's own theme again, so a project with its own isn't repainted.
A change to a project's theme reaches that project's pages alone.
The page keeps a copy of the last theme per project for the next load, so a project doesn't flash another's.

## Why

Someone who keeps a work project and a personal one open in two tabs wants to tell them apart at a glance, and a production dbt project may want a loud theme.
The project file is checked in with the project, so a team can also share a theme; whoever doesn't want that sets nothing and keeps theirs.

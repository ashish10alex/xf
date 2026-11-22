---
status: accepted
---

# xf web's settings page has an Overview and a Settings tab

The settings page has two tabs.
**Overview** is the project info (outside a project, **Tools & sign-in**: the tools and the Google accounts), read-only.
It starts with one line that sums up how things stand: the tool and its version, the Google sign-in, the branch and what it compares with, the editor, and how many settings the files set, which opens them.
Then the info's sections list their rows as the Dataform Tools extension's Project tab does: a fixed column of labels, the values beside them in monospace, paths in blue (a click copies one), notes dimmed after the value, and two columns on a wide window.
**Settings** are the editable keys, a row each: the title and what it does on the left, the field and where its value comes from on the right.
A green bar marks a key the scope's file sets, with its default and **reset**; a field set in no file shows the value in use, dimmed.
**Changed** in the sidebar shows only the keys the scope's file sets.

`o` and `s` switch the tabs (`tab` already switches Global and Project), `c` toggles Changed and, on the Overview, opens the changed settings.
The page opens on the tab it showed last.

The page's words are in the system's sans (`--sans`); monospace is kept for values, keys, paths, fields and the JSON editor.
It is the only page in sans; the rest of xf web stays monospace.

## Why

The page was hard to read at a glance.
Everything was monospace, so labels, values and help looked alike.
Each setting was a card that showed its value up to three times (a Current chip, a Default chip and the field), and the info under the settings was a card per section whose label column changed width from card to card.
The extension's Project tab, plain as it is, reads at once.
Splitting "is my setup OK?" from "change a setting" lets each be as plain as that.

## Considered options

Four mockups were compared on 2026-10-22; the user picked this one.

- **Inspector**: the extension's layout for the whole page, info first, then the settings as label and value rows that open to edit.
  The closest to the extension, but a setting's help and field are a click away.
- **Ledger**: one table of setting, value and who set it, with filters, and the info as read-only rows at the end.
  The densest, but it suits a phone less well and leaves the least room for help.
- **VS Code's settings editor**: every field always showing, with a modified bar. Familiar, but the longest scroll, close to what was there.

## Consequences

- The Overview's top line reads the info rows by their section and label (`binaries`, `google cloud` / `xf uses`, `git` / `branch`, `editor` / `connected`), as the sign-in banner already did.
  Renaming those in `internal/frontend/info.go` or `gcpauth` needs `statusItems` in `settings-view.js` changed too; `settings-overview.test.js` holds it.
- The info's `settings` section shows on neither tab: the Settings tab is it.
- Settings sections are owned by Go (`settingSections` in `cmd/xf/websettings.go`) and grouped in the sidebar under **Tool** (Dataform, dbt), **Warehouse** (BigQuery, Databricks, Snowflake) and **General** (Git, Editor, Appearance, App, Vim). Inside a project, the page hides warehouses other than `settingsAnswer.warehouse` (while keeping `Settings.View` unfiltered by warehouse so live settings stay intact), except that keys set in the active file or by a flag, Changed rows, and search hits always stay visible with a `not used by this project` note. Sections whose keys are all Global-only (Vim) collapse to a single `Global only · switch` line on the Project tab unless searched. If a dbt profile's target type can't be read before a compile succeeds, `warehouseKind()` falls back to BigQuery and Databricks/Snowflake keys stay hidden until searched.

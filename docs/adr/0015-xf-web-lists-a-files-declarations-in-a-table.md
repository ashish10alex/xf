---
status: accepted
---

# xf web lists a file's declarations in a table

The rows, a card for the selected declaration, and the run pane are changed by [0019](0019-xf-web-declarations-table-names-rows-and-has-a-card.md).

In xf web, a file of external actions alone (Dataform declarations, or dbt sources in a `sources.yml`), two or more of them, has a table at the top of pane 2's Actions pane: a row per declaration, in the order the file names them, its `project.dataset.name` and when its table was last updated.
The table is the whole pane: the selected declaration's details aren't repeated under it.
A file that also builds a table or view, or that declares one table, keeps the Actions pane as it was: a declaration shown alone.
The tests a `sources.yml` defines aren't rows.

- **Rows and lines.**
  A row's line is where the file names it: the name in quotes in a JavaScript file, a source table's `- name:` in YAML.
  A name a loop builds has no line; its row comes last and is shown dim.
- **The cursor.** Moving the source pane's cursor onto a name selects its row. Of names that share a line, it is the one the cursor is on.
- **Clicks and keys.** A click on a row puts the cursor on its name. j/k (and `]`/`[`) move through the rows and select without moving the cursor.
- **"updated".**
  Only the selected declaration's table is looked up, as before.
  A row keeps what an earlier selection found and is blank otherwise.
  An error shows up to its first colon, the whole of it on hover and in the columns tab.
- **The filter.** A filter box narrows the rows by name, and the table can be folded away. That is kept in the browser.
- **Row actions.**
  The row under the mouse and the selected row have copy, open in the BigQuery console, and preview.
  Preview opens Scratch on `SELECT * … LIMIT 100`, left to run, so its dry run shows the cost first.
- **Size.** The table is twelve rows tall and scrolls. When every row is in one project, the project is said once, above the rows.

Any declaration or source, in a table or not, shows its table's columns in pane 2's columns tab, from the same lookup: it has no SQL to dry run.
A declared view or materialized view shows its SQL, as BigQuery has it, in the sql tab and opens on it; anything else opens on its columns.
The tab the other actions had comes back when one of them is selected.

The TUI is in maintenance mode and keeps showing a declaration alone.

## Why

One `sources.js` can declare dozens of tables.
Showing each alone (`project.Graph.Siblings`) kept the Actions pane short, but left `/ $declaration` as the only way to see what a file declares. vscode-dataform-tools shows such a file as a table.
Following the cursor makes the table a map of the file rather than a second list to keep in step.
A declaration's SQL pane said only that it was defined elsewhere; its columns are what one wants of it.

## Considered options

- **The table in place of the run, SQL and columns panes.** They would have nothing to show until a row was picked.
- **The selected declaration's details under the table.** They repeated its row.
- **A tab of its own in pane 2.** One more stop for Tab on every file.
- **"updated" for the rows in view**, looked up as they scroll in.
  It was built first, then dropped: a file of declarations one can't read fills the table with errors and BigQuery with calls, for a date one wants of the declaration at hand.
- **Paging, as vscode-dataform-tools does.** The cursor's row could be on another page.
- **Keys for copy, console and preview.**
  The table has j/k, which the pane had.
  The row's buttons do the rest, and `u` and `y` act on the selected declaration as before.

## Consequences

- `/api/action` sends `externals` for such a file, with each row's line and its "updated" once known, and `tableType` for a declaration whose table has been looked up.
- The table lookup asks BigQuery for a view's query too.
- `/api/scratch/create` takes `form: "preview"`, which looks nothing up: a table the user can't see would fail the lookup `columns` makes.
- The page loads two more modules (`externals.js`, `externals-view.js`); the first-load budget is 43.

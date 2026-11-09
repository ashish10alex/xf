---
status: accepted
---

# xf web inspects a model's upstream tables on a page

xf web has a Dependency inspector page, a port of the Dependency Inspector of the Dataform Tools extension, at full parity, for Dataform and dbt projects.
It keeps the extension's full name because xf already calls pane 2 the inspector.
It lists a model and the tables upstream of it, gives each a filter, and dry runs or runs `SELECT * FROM <table> WHERE <filter>` on each, so one key (an order id, a date) can be followed through every table a model reads to find where its rows go missing or go wrong.

**Opening it.**
Only the palette's `>` command opens it, on the selected action; there is no key.
The page has its own model picker to change model without leaving.
It takes the whole window, as the Checks and settings pages do, and leaves at once with `ctrl+q` or its ×, as every page does (ADR 0030).
The grilling said `esc` and a y/enter question, which ADR 0030 had already replaced.
Leaving keeps the page as it was (each model's selection, search, dry runs and results), and the palette's *go back to <model>* returns to it, while *Dependency inspector* on another selected model opens that one; the tab's workspace (ADR 0008) keeps it over a reload, open again if it was, until the tab closes.

**What it lists.**
The model, then what it reads, breadth first, to a depth set on the page (1 to 20, 3 at first).
Upstream only: downstream tables are often aggregated and lack the filter's columns.
Assertions are left out, as the extension leaves them out.
A dbt ephemeral model, or an operation with no output, has no table: it gets no row, but the walk goes on through it to what it reads, and the graph draws it as a dashed node with the edges passing through.

**The page.**
Above: the model, depth, preset picker, the global filter and *apply to all*.
Then two tabs, `rows` and `graph`, sharing the selection and each row's status, and a search box beside them: it shows only the tables whose name or table has its text, fading the rest in the graph, and the header's tick ticks only those shown; *ticked only* beside it narrows them to the ticked rows, as the search does.
*+ add table* opens the palette's find action to add any other table (not an assertion or test) as a row of its own, marked `+`, in a graph column right of the model: a key often runs through a table the model doesn't read.
Added tables are kept by their names before dev settings, with the model's state and in its presets; each reads what an action that reads it would read, so defer to prod applies (one nothing reads, from dev).
A pasted filter is tidied outside quotes (each run of spaces and tabs to one space, of line breaks to one line break, none inside brackets or before a comma), as the global one is on leaving its box, and that box wraps to five lines: an id list pasted from a spreadsheet is otherwise unreadable.
A filter with a comment is left as typed.
Each row has a tick (whether *Dry run all* and *Run all* include it), its depth, where it is read from, its own filter, its dry run's bytes and cost, its run's row count, and buttons: dry run, run, schema, open the query in Scratch, copy the query, open the table in BigQuery, open the action's file (leaving the page).
The schema opens in a side panel drawn as pane 2's columns tab draws columns, from the same cached table metadata.
The graph is xf's own SVG: a column per depth, pan and zoom, a click selects a node's row, and each node shows its status.
Below both tabs, the selected row's result shows in Scratch's results grid (paging, column filters, search all rows); selecting another row shows its result.

**Keys.**
The page is for the mouse: every control is a button, a tick or a box.
Its only keys are `tab` (rows ↔ graph), `ctrl+c` (cancel the runs), `enter` in a filter box (dry run that row; in the global one, every ticked row; `shift+enter` starts a new line), `⌥⌘←/→` (`ctrl+alt` off a Mac: the result of the row before or after that has run, going round, as the result's ‹ › do; the log pane's tab keys, which the page has no use for), `z` (zoom the section last clicked, rows or graph, result or schema, to fill the page, as `z` zooms a pane; again to unzoom), `esc` (unzoom, else close what is open on the page: the run question, the presets menu, the schema panel, a selected cell; in the search box, clear it) and `ctrl+q` (leave).

**Which table.**
A row queries the table its dry run would read: with defer to prod on (ADR 0031), an upstream table that isn't built in dev is read from prod, and the row says `→ prod` as the source pane does.
The model itself is always read from dev.

**A filter a table can't take.**
When the global filter goes to every row and a table has no such column, its dry run fails with BigQuery's own error (`Unrecognized name`), and you untick the row or give it its own filter. xf doesn't read the filter to guess which tables have its columns.

**Running.**
*Run all* always dry runs the ticked rows first and asks, showing the total bytes and cost and marking each row with no filter `⚠ full scan`.
A single row runs at once, unless it has no filter: then it asks, with its dry run.
Runs go at most 4 at a time, the rest queued.
`ctrl+c` cancels the queued ones and the running jobs.
The runs are xf web's, as every run is: leaving the page leaves them running, and they hold off xf web's auto-exit.
Dry runs and runs are billed to the Dataform project's `defaultProject`, else the credentials' quota project, and the jobs are labelled `xf-inspector`.

**What is remembered.**
Per model, the project's session keeps the filters as last typed (saved or not), the depth, the ticks and each row's last BigQuery job.
Coming back, or reloading, shows them again and reads each result's rows back from BigQuery's temporary table, as Scratch's recent results do (ADR 0029).
No rows are written to disk.

**Presets.**
As in the extension: a model has any number of named presets, each holding the global filter, *apply to all*, and each row's tick and filter, with a description and when it was saved.
*Save* overwrites the preset being edited, *Save as new* adds one, and the picker deletes one.
Opening a model loads its latest preset.

- A preset is keyed by the action's name before dev settings apply, not by its table: in Dataform the canonical target's dataset and name (what `ref()` matches, and what defer to prod matches prod by), in dbt the node's name as `ref()` gives it (`package.name`, with `.v2` for a version) or, for a source, `source:source.name`.
  (dbt's unique id is not in xf's graph; these name the node as well.)
  A preset then still matches after a schema suffix, table prefix, dbt target or defer to prod changes the table, and a declaration's source table matches by its declared name.
- They are personal: `<git root of the main checkout>/.xf/inspector/filters.json`, in a folder with its own `.gitignore` of `*`, as `.xf/tmp` and `.xf/worktrees` have.
  Each project in the repository has its own part of the file, by its folder from the git root, and every worktree of the repository (ADR 0032) reads and writes the same file.
- The extension's `.vscode-dataform-tools/dependency-inspector-filters.json` is neither read nor written.

The TUI doesn't get it.

## Why

The extension's inspector is the way its users debug a model's data, and xf web had nothing that runs a filtered query across a chain of tables: Scratch runs one query you write, and pane 2's lineage tab only lists the chain.
The user chose full parity over a smaller tool, so users moving from VS Code find the same one.

A page, as Checks is, gives each row room for its filter, status and buttons, and the results grid room for its columns.
The rows and the graph are tabs, as in the extension; side by side, both were cramped on a laptop and left the results little height.

The extension draws its graph with React Flow and dagre. xf web's page has no build step, and React Flow needs React.
The walk already puts each table at a depth, so drawing a column per depth in SVG is little code and adds no vendor file or first-load cost; it doesn't minimise crossing edges, which matters only on wide graphs.

xf is careful with BigQuery spend, and *Run all* can scan every table of a chain: so it always shows what it would cost first.
Capping runs at 4 keeps a deep chain from hitting slot and quota limits, which the extension's run-everything-at-once can.

Following defer to prod means the inspector reads what the model's own dry run and run read; reading only dev, as the extension does, fails on every table not built in dev, which is the case defer exists for.

Keying presets by table id, as the extension does, breaks them whenever the table's name changes with who runs it or where: a suffix, a target, defer.
The action's name doesn't change.
The user wanted presets personal rather than committed, and kept at the main checkout so that opening a branch in a worktree doesn't lose them.

## Considered options

- **Built on Scratch**: write one SELECT per table into a scratch. Little new UI, but no view of the whole chain or of each table's status.
- **A tab in pane 2**: always beside the action, but pane 2 is narrow for rows of filters and a results grid.
- **Vendoring dagre**: the extension's layout, for about 100 KB of vendor code on this page alone.
- **Both directions**: following a key into the tables that read the model too. Twice the rows and the scans, and many filters don't apply downstream.
- **Results as stacked cards**, as the extension shows them: every result at once, but each grid small and the page long.
- **A full key map** (j/k, space, d/D, r/R and so on): the user wanted the page driven by the mouse, as xf web alone has it.
- **The extension's guard**: *Run all* blocked while a ticked row has no filter, with no dry run.
  It stops full scans but says nothing about a filtered query that is still expensive.
- **Checking columns before a dry run** to untick rows that lack the filter's columns: it would have to parse the filter, the kind of guessing the user has turned down before (Scratch completion).
- **Sharing presets in the repository**, or in the extension's file so one set works in both tools: the user chose personal presets in xf's own file.

## Consequences

- The extension's presets don't carry over to xf; a team that wants shared presets copies the file.
- A preset whose action is renamed or removed no longer matches; its filters for tables that left the chain stay in the file unused.
- The page reuses Scratch's results grid and pane 2's column drawing, so both are shaped to be used outside their own views.
- `git clean -fdx` removes the presets, as it removes any ignored file.

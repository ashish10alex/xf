---
status: accepted
---

# xf web's declarations table names its rows briefly and has a card for the selected one

This changes three parts of ADR 0015, for a file of declarations in xf web; the rest of 0015 stands.

- **Rows.** A row is the declaration's name alone. Its `project.dataset` is said once above the rows that share it (a line before each group when the file has more than one), and the project gives way first when there is no room. A row has a dot for how old its table is (green under two days, blue under a month, dim under six, amber beyond; red for an error; hollow until the table was looked up) and, right-aligned, the short "updated" it had: `11mo ago`, or an error up to its first colon. Old is amber, not red: a source that updates monthly is not a fault.
- **The card.** Under the table, a card for the selected declaration: its name, table type, full id, when its table was updated, how many columns and users it has (a tag that opens the lineage), its line, and copy, console, go to line and a Preview button. It adds to the row what the row has no room for.
- **No run pane.** A declaration has nothing to run, so pane 2 doesn't show the run pane for one. Preview, which fills the place of the run button, is on the card.
- **Titles.** The Actions pane is titled "declarations", and pane 2's title is the declaration's name: the file's path is pane 1's.

## Why

Every row repeated `project.dataset.`, so the part that tells rows apart was cut off ("SPEC_ORD_HIST…"). Rows not yet looked up were blank, with no hint why; a pink "not updated today" on a table that updates monthly cried wolf. The run pane said only that declarations can't be run. Three layouts were compared as mockups (a list over a card, a card with the lineage always open and a jump box, a sortable audit table); the user chose the first.

## Considered options

- **The card as ADR 0015 rejected it.** It repeated the row; this one holds what the row can't: the full id, the type, the counts and the buttons.
- **Sorting by age, chips for stale, errors and unused, a used-by count on each row.** Only the selected declaration's table is looked up (0015), and only its lineage is read, so no row but the selected one has the figures to sort or count by.
- **A jump box in place of the list**, with the lineage always open. The list stays: it is the map of the file that the cursor follows.

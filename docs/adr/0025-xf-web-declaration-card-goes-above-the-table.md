---
status: accepted
---

# xf web puts the declaration's card above the table

This changes one part of ADR 0019: the card was under the table, and now it is above it. The rest of 0019 stands.

A file of declarations in xf web reads from the top down: the card for the selected declaration, then the table of the file's declarations, then pane 2's target and its sql | columns | lineage tabs. The card stays when the table is collapsed.

## Why

The card is what the user looks at: it names the selected declaration in full and holds its buttons. Under a table of up to twelve rows it sat far from the top of the pane, and moved down or up as the table grew or shrank. At the top it stays in one place, and the table is the list the cursor moves in.

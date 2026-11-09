---
status: accepted
---

# xf web's declarations table shows each row's full name

This changes the rows of ADR 0019: a row was the declaration's name alone, under a line naming the `project.dataset` of the rows after it.
Now a row is the full name, `project.dataset.table`, and there are no group lines.
The table's title is always "declarations in this file".
The rest of 0019 stands.

The project and dataset are dim and the table's name is not, so the part that tells rows apart stands out.
When there is no room, the project gives way first, then the dataset; the table's name is never cut.

## Why

The user asked for it.
Under group lines, a row of a file that declares tables from many datasets read as a bare name, and which dataset it was in took a look up the table.
The full name is what is copied, filtered on and searched for in BigQuery.

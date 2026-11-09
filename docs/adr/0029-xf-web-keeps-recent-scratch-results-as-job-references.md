---
status: accepted
---

# xf web keeps recent scratch results as BigQuery job references

xf web keeps the last 5 scratch runs that returned rows so their results can be opened again after switching scratches, reloading or restarting.
It keeps each run's BigQuery job (project, location, id), the SQL that ran, its times and row count in `runs.json` beside the scratches.
It writes no rows: opening a kept run reads its rows again from the temporary table BigQuery put the result in.

## Why

BigQuery already holds every query's result for about 24 hours, and reading it back (`jobs.getQueryResults`) is not billed.
Reading from there keeps everything the result view does working (paging, search and sort of all rows, CSV export), since those already run over that table.
Rows on disk would put query data, which may be sensitive, in plain files, and a result can be gigabytes.
The user chose this over keeping the first page of rows, or every row, on disk.

## Consequences

- A result older than about a day is gone; the list marks it `expired` and opening it says to run the query again.
- 5 is a list length, not a storage limit: a kept run costs a few hundred bytes plus its SQL. Raising `maxScratchRuns` costs nothing but a longer list.

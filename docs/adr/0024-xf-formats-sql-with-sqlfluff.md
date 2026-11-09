---
status: accepted
---

# xf formats a file's SQL with sqlfluff

In xf web, *Format file* (in the ⌘P commands), the ex command `:format` (`:fmt`) and the source pane's right-click menu run `sqlfluff format` over the SQL of the file in the editor, and `xf lsp` answers `textDocument/formatting` the same way.
Scratch queries take `:format` too, and *Format: query with sqlfluff* in Scratch's ⇧⌘P commands.
The `vimFormat` setting binds a key for both editors; it is the one vim key with no default, so nothing is mapped until it is set.

- **On demand only.** Nothing formats on save, and `sourceAutosaveMs` doesn't start one. A format takes about 0.4 s, nearly all of it sqlfluff starting up.
- **The buffer, not the file.**
  The page sends its unsaved text; the server returns the formatted text, and the page applies the difference in one undo step (cursor kept).
  It doesn't save: the user reads the result, then `:w`.
- **Which SQL.**
  In a `.sqlx` file: the main query and the body of each `pre_operations` and `post_operations` block, each formatted where it sits.
  The `config` block is formatted too, without sqlfluff (see *The config block* below).
  Everything else (the `js` blocks, the whitespace and comments between blocks, the order of blocks) stays byte for byte as it was.
  A Scratch query is one query.
  Dataform only: a dbt model's Jinja isn't hidden, so dbt files are refused with a message.
- **`${ … }` is hidden, not templated.**
  Before sqlfluff sees a part, each `${ … }` (found by `navigate.Regions`' rules, so one in a SQL comment is left as the comment) becomes an identifier of the same length where it fits (`_xf0_____`), and goes back afterwards, matched without regard to case, since a capitalisation rule may have changed it. sqlfluff gets `--templater raw --dialect bigquery`, so the project's `.sqlfluff` needs none of the `templater = placeholder` and `param_regex` lines the Dataform Tools extension asks for, and a `${when(incremental(), …)}` in a column's place no longer stops the format.
  If a part then won't parse and a `${ }` has a line to itself (a clause: `${when(incremental(), `WHERE …`)}`), where no identifier parses, the part is tried again with those as `-- _xf1_` comments, which parse anywhere.
- **`format`, not `fix`.**
  `sqlfluff format` runs only sqlfluff's layout and capitalisation rules, which never change what a query means.
  `fix` adds `AS`, rewrites `GROUP BY` and leaves "unfixable" errors; it isn't offered.
- **The config.** sqlfluff's own lookup: a `.sqlfluff`, `tox.ini`, `setup.cfg` or `pyproject.toml` in the file's folder or above it, up to the project's root.
  `sqlfluffConfig` (a path) names a file to use in place of those (`--ignore-local-config`, since `--config` alone is laid over the project's own).
  With neither, a default compiled into xf is used: the `.sqlfluff` the Dataform Tools extension writes, a style its users know works (BigQuery, 2-space indent, leading commas, upper-case keywords, functions, types and literals, aligned aliases, `where` on a line of its own), with one change and less its last section.
  The change: the extension upper-cases unquoted identifiers, and xf excludes that rule (CP02) so they stay as written, since a BigQuery dataset or table name is case-sensitive and `FROM my_dataset.Orders` would become `MY_DATASET.ORDERS`.
  CP02's `consistent` isn't enough: it lower-cases `Orders` to match the query's other names.
  That section, `[sqlfluff:templater:placeholder]`, hides `${ … }` with a recursive `param_regex` and blanks `when`, `config` and `pre_operations`; xf hides them itself and runs `--templater raw`, so it and `templater = placeholder` are left out.
  The same lines in a project's own copy are ignored, and can stay for the extension. xf never writes a config into the repo and never downloads one.
- **The binary.**
  `sqlfluffBin`, else the `sqlfluff` of the project's `.venv/` or `venv/`, else the one on `PATH`.
  Missing, the page says `sqlfluff not found: uv tool install sqlfluff` once per project, and the file is untouched. xf never installs it.
- **A part that doesn't parse.**
  Nothing changes.
  The message gives sqlfluff's first parse error with its line and column in the file the user has open.
  No compile or dry run is needed first.
- **Not part of this.**
  Lint squiggles, format on save, dbt, sqruff (the call goes through one function, so it could be added), range formatting, `js` blocks, and the TUI.

## The config block

Added 2026-10-25, at the user's request.
A `config` block's body is JavaScript, nearly always one object literal, so xf formats it with a small formatter of its own in Go (`format.Config`): no Node, no prettier, no download.

- **The style.**
  One property to a line, two spaces to a level (from the indent of the line `config` is on), `key: value`.
  A nested object (`bigquery`, `assertions`, `columns`) always takes a line per property; `{}` and `[]` stay as they are.
  An array goes on one line when it fits in 80 columns and holds no comment, else a line per item.
  No comma after the last item.
  Quotes and keys stay as written.
  Comments stay, on their own line or at the end of one, and one blank line stays where there were any.
  A one-line `config { type: "table" }` opens to three lines.
- **What it doesn't read, it keeps.**
  Any value that isn't a literal, an array or an object (a call, an arrow function, a ternary, `-1`, a template literal, a regex) is kept as written, lines and all.
- **Checked, else refused.**
  The output's tokens must be the input's, less the commas that only end a list (a comment may move past a comma).
  A body it can't read (a missing `,`, a `;`, a comment between a key and its value, a string never closed) is refused with nothing changed, and the message gives the line and column in the file.
- **Apart from the SQL.**
  The config block and the SQL are formatted apart: with no sqlfluff, or SQL that won't parse, the config block is still formatted, and the page (or the editor, for `xf lsp`) says `formatted the config block, not the SQL: …`.
  The other way round too.
  When neither changed anything, the error is shown alone as before.
  A file with only a config block needs no sqlfluff at all.

## Why

The Dataform Tools extension formats with `sqlfluff fix` and works, but each of its choices cost something.
It compiles and dry-runs the file first and refuses on any error, so a half-written file can't be formatted.
It runs through a temp file shared by every window.
It downloads a `.sqlfluff` from GitHub into the repo.
Its `placeholder` templater hides `${ … }` with a regex, which fails for a JS expression in a column's place.
It formats the main query only, and puts the other blocks back in an order a setting chooses.
The user asked for a better way.

## Considered options

- **`sqlfluff fix`**, as the extension: it also rewrites SQL, which a formatter shouldn't.
- **sqruff**, which starts in 17 ms: it reads `.sqruff`, has its own rules, and rewrote `${ref("users")}` into `${ref ("users") }`.
  Hiding the `${ … }` would fix that, but two tools double what must be tested.
  The user chose sqlfluff.
- **Formatting the compiled SQL**: there's no way back from it to the source.
- **Format on save**: the user doesn't want a 0.4 s step on every save.
- **Formatting `config` and `js`**: first left out, since Go has no JavaScript formatter.
  `config` was added later with a formatter of xf's own (*The config block*).
  `js` holds statements and functions, far more than an object literal, and stays as written. esbuild's Go library prints JavaScript but drops comments; prettier needs Node.
- **A default config written to `.xf/` or downloaded**, as the extension does: an untracked file in Source Control that nobody asked for (as ADR 0018 chose for `pyrefly.toml`).

## Consequences

- Column alignment (`AS` in a SELECT) is computed with the hidden text, so it is right only when the placeholder is as long as the `${ … }`.
  A `${x}` (4 characters) can't be, since the shortest name is 5, and its alias may sit a column off.
- The project's `.sqlfluff` shared with the extension or CI keeps working: the command-line templater and dialect override its own.
- `xf lsp` runs sqlfluff in the language server's process; the formatting request waits for it (30 s at most).

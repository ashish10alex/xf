---
status: accepted
---

# xf web asks for prod as a form

`prodCompilerOptions` is a string of `dataform compile` flags, and xf web asked for it as one: a text box with `--default-database=acme-prod` as its only hint.
A user had to know Dataform's flag names to say what is usually one thing, the prod project. xf web now shows it as a form, in the *compare with prod* dialog and on the settings page alike:

- **Prod project** writes `--default-database`, **Prod dataset** `--default-schema`, and a folded **more flags** box keeps every other flag as written.
- **The config doesn't change.**
  The form reads and writes the one string the files, `--prod-compiler-options`, `xf cost` and `xf impact` already use.
  Reading it, `--default-database` and `--default-schema` go to their boxes in either form (`=x` or ` x`); everything else goes to more flags.
  Saving writes the `=` form, boxes first.
  A string with a quote not closed goes whole into more flags.
- **An empty box says what it gives.** Its placeholder is the value in `workflow_settings.yaml`, as `same as dev: xf_example` when dev uses it too.
- **Every box empty is "the default targets are prod"** (`""`).
  The form says so, and the separate chip and the dialog's radio are gone.
  On the settings page, where nothing sets the key, empty boxes write nothing; a link writes `""`.
- **A warning, with no compile, when prod would name dev's tables:** the prod project and dataset (the form's, else the workflow settings') against xf's own compile's project, dataset, prefix and suffixes.
  A name flag in more flags (`--table-prefix`, `--schema-suffix`, `--database-suffix`) turns the warning off, as xf can't tell.
  The dialog still compiles before it saves; the settings page saves on leaving the form, without compiling.
- **dbt:**
  `dbtProdTarget` on the settings page is a pick of the profile's targets (from the `profiles.yml` dbt finds), the one xf compiles with marked `dev`, and picking it gives the same warning.
  With no profile read, it stays a text box.

`/api/target` gives the page both sets of names: the workflow settings', read line by line from the file, and the dev compile's `projectConfig`.

The TUI keeps its text prompt.

## Why

Prod most often differs from dev by its project alone, sometimes by its dataset.
A box named for that, with dev's value beside it, needs no flag knowledge, and the warning catches the commonest slip, the dev project typed in, which defer to prod otherwise reports only later ("prod names the same tables as dev").

Keeping one string means nothing migrates: existing files, CI flags and the TUI go on as they are, and flags the form has no box for still work.

## Considered options

- **New config keys** (`prodProject`, `prodDataset`) in place of the string: cleaner files, but a migration, new CLI flags, and two ways to say the same thing.
- **The text box with completion of the flag names:** the least change, but the user still writes flags.
- **A box for every name flag** (assertion dataset, location, prefix, suffixes, vars): complete, but a long form for a one-box answer; more flags covers the rest.
- **Suggesting a prod project:** a list of the projects the Google login sees (a call, and long lists at large organisations), or `dev` swapped for `prod` in dev's name (wrong whenever names don't follow the pattern).
  The user chose to show dev's values only.
- **Compiling on the settings page too:** catches bad flags, at a compile per save.

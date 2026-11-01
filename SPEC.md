We're starting a new project: a lazygit-style terminal UI for Dataform, written in Go with Bubble Tea.

Read `docs/adr/0001-lazygit-style-tui-in-go-and-bubble-tea.md` first. It's the accepted ADR and defines the stack and the v1 scope. Build only what's under "v1: the basics"; everything under "Later" stays out, even if it looks easy to add.

## Reference material
- The VS Code extension this is inspired by lives at `~/vscode-dataform-tools`. Use it to look things up; don't copy its architecture. Useful files:
  - `src/types.ts`: TypeScript types for `dataform compile --json` output (`DataformCompiledJson`, `Table`, `Target`, `Operation`, `Assertion`, `Declarations`, `Test`).
  - `packages/dataform-graph-cli/src/compile.ts`: how the extension runs `dataform compile --json` and extracts the JSON. Some older Dataform versions (e.g. 2.9) print a logger JSON object before the compile result, so stdout can hold two JSON values back to back. Port the "find the object that looks like a compile result" logic.
  - `src/shared/buildDependencyGraph.ts`: how dependencies and dependents are derived from `dependencyTargets`.
  - `CONTEXT.md`: domain terms. Use the same names in code (Target, Action, Changed Action, and so on).
- Compile output facts that matter for v1:
  - Top-level keys: `tables`, `operations`, `assertions`, `declarations`, `tests`, `notebooks`, `targets`, `projectConfig`, `graphErrors`, and optionally `dataformCoreVersion`.
  - A target is `{database, schema, name}`. Identify actions by `database.schema.name`.
  - A table has `type` (table, view or incremental), `query`, `incrementalQuery`, `preOps`, `postOps`, `incrementalPreOps`, `dependencyTargets`, `tags` and `fileName`.
  - Compile errors come back in `graphErrors.compilationErrors` (each with `fileName` and `message`), not as a crash. Show them in the UI.
- If you have a real Dataform project to test against on your machine, save its compile output as a local (gitignored) test fixture; `just fixtures-local` does this from `$DFT_LOCAL_PROJECT`.

## How I want to work
1. Before any code, enter plan mode and propose:
   - the package layout (for example `cmd/<bin>/`, `internal/dataform` for compile, parse, graph and run, and `internal/ui` for the Bubble Tea models and panes);
   - the core Go types for the compiled graph;
   - the messages and commands for compiling and for streaming run output;
   - the pane layout and keymap.
   Ask me about the module path and the binary name instead of guessing (my suggestion: `github.com/ashish10alex/dataform-tui` and `dftui`).
2. Build a walking skeleton first: compile a project, list its actions, show the selected action's SQL. Make that work end to end before adding lineage, then run.
3. Keep the Dataform logic in `internal/dataform` free of Bubble Tea imports, and table-test it against JSON fixtures in `testdata/`. Include at least one fixture with incremental tables and one with compile errors. Test the UI with `teatest` where it's practical.
4. Find the `dataform` binary through `--dataform-bin`, then `$DATAFORM_BIN`, then `PATH`, and print a clear error if it isn't found. Run it from the project root, the nearest directory containing `workflow_settings.yaml`.
5. Never block the UI. Compiling and running are `tea.Cmd`s. Stream run output line by line as messages, allow one run at a time, and make `ctrl+c` cancel the child process (not quit the app) while a run is in progress.
6. After each step, run `go vet ./...` and `go test ./...` and tell me what actually passed. Don't commit unless I ask.

Set up a `justfile` with `build`, `run`, `test` and `lint` recipes from the start.


## How it might look
Im expecting it to look something like lazygit: panes for actions, lineage, compiled SQL and runs side by side.
But if you can suggest something better im open to that as well


## What Im unsure of

is how i will edit my files Im open to using neovim and in a split i can have this tui open but how will i get the feedback on save to the tui . Is it even possible



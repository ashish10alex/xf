---
status: accepted
---

# Build a lazygit-style Dataform TUI in Go with Bubble Tea, starting with the basics

vscode-dataform-tools (~/vscode-dataform-tools) only works inside VS Code. A "Terminal" mockup of its Compiled Query panel (box-drawn panes, single-key actions, a streaming run log) showed that a
keyboard-driven TUI in the style of lazygit or k9s would suit the everyday loop of reading compiled SQL,
checking lineage and running an action. We will build it as a standalone Go program using Bubble Tea, the stack behind most tools in this category, so it starts instantly,
ships as a single binary and gets the polish of Bubbles (list, viewport, text input) and Lip Gloss.
It talks to Dataform only through the `dataform` CLI and does not share code with the extension.

v1 is deliberately small. Anything not listed under v1 below waits until the basics feel right.

## v1: the basics

- **Compile** the project by running `dataform compile --json` from the project root (the directory with `workflow_settings.yaml`). `r` recompiles, and compile errors are shown in a pane instead of crashing the program.
- **Actions pane:** every action with its type and target, filtered with `/`.
- **SQL pane:** the compiled query of the selected action. For incremental tables, `v` switches between the incremental and non-incremental queries, and pre- and post-operations are shown too.
- **Lineage pane:** dependencies and dependents of the selected action as a tree. `enter` on an entry jumps to that action.
- **Run** the selected action with `dataform run --actions <target>`. Toggles for `--include-deps`, `--include-dependents` and `--full-refresh` are shown on screen, and the command's output streams into a log pane. Only one run at a time, and `ctrl+c` cancels it.
- **Keys** follow lazygit: panes are numbered, `tab` moves between them, a footer always shows the keys for the focused pane, and `?` opens help.

## Later, not in v1

Dry-run bytes and cost, Changed Actions, Defer to prod (with Proxy Views), tags and running by tag, Dataform API compiles and workflow invocations, output schema, data preview, and opening files in `$EDITOR`.

## Consequences

- The `dataform` CLI (an npm package) is still a prerequisite, so users still need Node, but the TUI itself does not. It should find the binary on `PATH` or through `--dataform-bin` / `$DATAFORM_BIN`, the same convention `dataform-graph` uses.
- The TUI depends on the shape of `dataform compile --json` output. Keep a few real compiled-JSON files as test fixtures, from different `@dataform/core` versions, so a format change fails a test rather than the UI.
- Once later features arrive, logic that exists in the extension will be written a second time in Go, above all Changed Action detection and the Deferral rules. The extension stays the reference: the TUI keeps its domain language (Changed Action, Deferral, Deferred Action, Stale Deferral, Proxy View, Dev Target, Prod Target) and follows the extension's ADR 0001 (defer by rewriting SQL for reads and creating Proxy Views for runs). Port each rule together with its test cases so the two implementations don't drift.
- Following Bubble Tea's model, compiling and running happen in commands that send messages back to the UI. Run output is read line by line from the child process and sent as messages, so the UI never blocks on a subprocess.

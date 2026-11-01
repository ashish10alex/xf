---
status: accepted
---

# xf web defers to prod

xf web gets the extension's **defer to prod**, like dbt's `--defer`. When it is on, an upstream table that is not built in dev is read from prod. This applies to what xf runs itself (pane 2's SQL and columns, the source pane's dry run) and to runs. The extension's design is the model (vscode-dataform-tools, its ADR 0001 and `src/defer/`). This ADR records where xf follows it and where it differs.

## Terms

- **Selected Action:** an action that a run or a dry run executes: the ones you picked, plus the dependencies or dependents the run includes.
- **Deferral:** reading an upstream action from its Prod Target instead of its Dev Target. It applies when the action is not a Selected Action, its Dev Target does not exist, and its Prod Target exists and can be read.
- **Deferred Action:** an upstream action that a Deferral applies to.
- **Stale Deferral:** a Deferred Action that is also a Changed Action, so its Prod Target does not have the branch's code.
- **Proxy View:** a view at a Deferred Action's Dev Target that selects everything from its Prod Target, so that a run reads prod through dev names.

## Decision

**The engine works it out, xf web and `xf lsp` apply it.** The engine works out the Deferral once per compile and keeps it. xf web and `xf lsp` dry run the rewritten SQL. nvim gets the rewritten dry runs only, so it stops showing "not found" for upstream tables you have not built. The labels stay in xf web. The TUI is in maintenance mode and does not change.

**The switch.** A `deferToProd` config key sets the default; it is off when nothing sets it. You turn it on and off in xf web from the palette or by clicking its item in the status line. There is no key for it. The engine remembers the choice per project, in xf's state and not in a committed file, so it never changes the setting for the team. It is the engine's state and not part of `backend.EngineName`, so xf web and nvim keep one engine, and nvim follows the switch.

**Dataform: prod is `prodCompilerOptions`.** Dev is xf's compile, with `tablePrefix`, `schemaSuffix` and `databaseSuffix`. Prod is a compile with `prodCompilerOptions`. When nothing sets that, turning defer on asks with the same prompt as `I` and `C` (ADR 0028). Esc there says that the default targets are prod. If prod then names the same tables as dev, xf web says so and defer is not applied. The prod compile is kept per options, settings file and day, and it is compiled again when an action is not in it. In API mode, the pushed commit is compiled, as query cost does.

**Dataform: what is deferred.** The upstream actions of the Selected Actions are the candidates. These are left out: Selected Actions, operations without output, and actions whose Prod Target is their Dev Target. BigQuery then says, for each candidate:

- **Built in dev:** it is read from dev. The label gives the age of the dev table.
- **Not built in dev, and in prod:** it is deferred.
- **In neither, or prod cannot be read:** a warning.

An `Access Denied` on a Prod Target marks that table as unreadable, and the next lookup reads it from dev. A Deferral that is also a Changed Action is flagged as stale. The flag uses the engine's Changed Actions. When they are not cached, xf works them out in the background and the flag arrives later.

**Dataform: reads.** In the compiled SQL that xf dry runs and shows, each Deferred Action's backtick-quoted Dev Target is replaced with its Prod Target. Line numbers do not change, so dry-run errors stay on their lines. Scratch queries are SQL you write, so xf does not rewrite them.

**Dataform: runs, in the same release as reads.** Before a CLI or API run starts, xf makes a Proxy View at each Deferred Action's Dev Target. It uses the extension's label (`dataform_tools_proxy`) and the same view SQL, so each tool finds, uses again and removes the other's views. xf never makes a dev dataset, and it never changes IAM. All the extension's checks before a run come as xf web dialogs:

- **A table that cannot be deferred** (in neither place, prod not readable, or no dev dataset) stops the run. The dialog offers to run with dependencies.
- **Stale Deferrals:** the dialog offers to build the changed ones first (only those are added to the run), to run with dependencies, or to run anyway.
- **An API run** asks once per repository whether the runner (the repository's service account) can read the prod datasets.
- **A function or procedure** cannot be a view, so the run builds it in dev.
- **With defer off,** a Proxy View left from an earlier run still makes the run read prod. The run warns and offers to run with dependencies.

**Cleanup.** A palette command removes the project's Proxy Views. When you turn defer off, xf web offers to remove them. The next real dev build of an action replaces its Proxy View in any case.

**dbt: dbt's own `--defer`.** The prod state is a `dbt parse` of the merge-base with `dbtProdTarget`, kept per commit. When defer is on, the engine compiles, and runs run, with `--defer --defer-state <that manifest>`. Turning the switch on or off compiles again. dbt decides which refs go to prod. xf finds out what dbt did from the compiled SQL, which names either the prod relation or the dev relation. It asks BigQuery nothing, and it makes no views. The dialogs before a run are for Dataform. A dbt run only shows, in its preview, what dbt will read from prod.

**Dataform: query cost reads new tables from dev.** Query cost already dry runs prod, so defer changes it the other way round. A table the branch adds is not in prod yet, so every dry run that reads it fails. With defer on, such a table that is built in dev is read from dev, and the row says so. The estimate is sized as dev has the table. A table that is missing in prod and that the branch does not add is never read from dev, because that case needs checking. Separately, with defer on or off, an incremental table whose own table is not built yet is priced as its full build, because that is what its first run does.

**What you see.**

- After each `ref()` in the source editor, a label: `→ prod`, `⚠ prod · changed on this branch`, `✓ dev · built 12 min ago`, `⚠ not built in dev or prod`, or `⚠ no prod access`. A hover gives both tables and a link to the prod table.
- In pane 2, a list of the same tables.
- In the status line, whether defer is on.
- In the run preview, "reads N upstream tables from prod".
- In the run history, whether a run was deferred and which tables it read from prod. A rerun from the history uses the state that was recorded.

## Why

- **Your branch's models can be dry run and run without building everything upstream first.** In dbt, `--defer` already does this. In Dataform, a dev schema with only some tables built gives "not found" errors on every dry run, and a run fails too.
- **The engine owns it** because nvim's diagnostics have the same "not found" errors, and one engine already serves both front ends (ADR 0001).
- **The switch is not committed.** Defer is your own way of working, and toggling it should not make a diff for the team.
- **Reads and runs come together.** Pane 2 says "read from prod" for a table. If a run then fails because that table is not in dev, that is worse than no defer at all.
- **The extension's Proxy View label** lets someone who uses both tools on one project clean up in either tool.

## Considered options

- **Only in xf web, or worked out in the engine and used only by xf web.** That is less wiring, but nvim keeps the false errors, and the logic has to move later.
- **The switch in the user file, or the project file.** The user file is global, so turning defer on in one project turns it on in all of them. The project file is shared and committed.
- **Reads first, runs later, as the extension did.** Rejected because of the inconsistency above.
- **dbt with the same rewrite and Proxy Views as Dataform.** That gives one rule for both tools and does not change the compile. But it is not what dbt users expect from `--defer`, and it makes views where dbt users do not expect them.
- **xf's own Proxy View label.** Then each tool would take the other's views for real dev tables.
- **Labels in nvim as inlay hints.** Left for later. The rewritten dry runs give nvim most of the value.
- **Remove Proxy Views each time defer goes off.** Rejected: the next deferred run makes them again, and a run with defer off already warns about them.

## Consequences

- The engine has a Deferral for the Selected Actions of the action on show and for a run. Remote clients ask for it and set the switch, so `remote.ProtocolVersion` goes up.
- A Dataform project with defer on makes one more compile (with `prodCompilerOptions`) and makes BigQuery lookups (table lists per dataset, kept briefly) for each new Deferral.
- A run with defer on writes views into dev datasets. The identity that runs it needs read access to the prod datasets it defers to. For API runs this is the repository's service account, which xf cannot check, so it asks.
- In a dbt project, turning the switch on or off recompiles. While defer is on, the compile itself has prod names, for xf web and nvim alike.
- The TUI shares the engine but does not use the Deferral. A TUI run with defer on reads dev, or prod through Proxy Views that are still there.
- This replaces the "Later, not in v1" mention of defer to prod in ADR 0001.

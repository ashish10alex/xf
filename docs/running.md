# Running and branches

## Run what your branch changed

`R` lists the actions whose compiled SQL or settings differ from where your branch left `main`, like dbt's `state:modified`, and `r` (or `enter`) runs them.
Every file starts checked: `space` unchecks the file under the cursor and `x` toggles them all.
Because it compares compiled output, a change in `includes/` counts too.
The first `R` of the day compiles that commit in the background (about as long as a normal compile); after that, changed actions are marked with `●` in `/` and in the Actions pane, updating as you save.

It compares with `origin/main`, or your local `main` when there's no remote, and never fetches.
The status line says what it compares with (`⎇ feat vs main`).
To compare with another branch, press `b` in `R`, `I` or `C` (in xf web, or click the branch in R's title, the Checks page or the status line): it lists your branches, the base first, each aged by origin's copy, and `ctrl+r` fetches origin.
The pick applies at once to `R`, `I` and `C`, and is saved as the project's `defaultBranch` in `.xf/config.json` (taken out again when it matches your own config or `main`); another xf open on the project picks it up when it next starts.
With `a` on (API runs), it compares what you pushed, since that is what Dataform runs.

A renamed file whose action takes its name from the file gives the action a new name, so the table it builds is a new one.
When git calls the file a rename (`git diff -M`, as GitLab and GitHub do, so a file that was also edited counts as long as most of it is the same), the new action is marked `renamed`.
Its file shows the old one, such as `← 0200_OLD_NAME.sqlx (95%)`, and the old action isn't listed again under the removed ones.
This works for a rename that isn't staged yet too: xf marks the new file in a copy of the index, never in yours.
A file that declares several actions isn't paired, and neither are the assertions generated from a config.

## Run tags

`T` lists the project's tags, each with how many actions it would run.
It is a search box: type to filter the tags, `space` checks the one under the cursor (the search stays, so you can check several matches) and `enter` runs every action that has any of the checked tags.
Nothing starts checked and nothing runs until you check a tag; a tag the search hides stays checked, and the checked tags are named under the list.

The run uses the Run pane's options (`d`, `D`, `f`, and `a` in a Dataform project), shown under the list.
Every letter is the search, so `esc` leaves it for the options: there `d`, `D`, `f` and `a` toggle as in the Run pane, `space`, `enter` and `j`/`k` still work, `/` goes back to the search and `esc` again closes.
A full refresh asks first, as `r` does.
The tool selects the actions, so the command stays short: `dataform run --tags daily hourly`, `includedTags` for an API run, and `dbt build --select tag:daily tag:hourly`.
An API run compiles the pushed branch, so a tag you haven't pushed isn't in it, though xf counts it.
`> Run tags` in the palette opens the same overlay.

## Switch branch

The status line names the branch your project's checkout is on, and how it differs from `origin/<branch>` as of your last fetch: `⎇ main ●3 ↑2 ↓1` is 3 uncommitted files, 2 commits you haven't pushed and 1 you haven't pulled, each shown only when it isn't zero.
A detached HEAD shows `⎇ (detached abc1234)`, and outside a git repository there is nothing.
It follows a switch, commit or fetch you make in a terminal at once, and in xf web hovering it lists the files and commits it counts.
The projects list (`xf` or `xf web` started outside a project) shows each project's branch too.

`B` (or `> Git: switch branch` or `> Git: checkout branch`, or a click on the branch in xf web) lists your local branches and those only `origin` has, newest commit first, each marked `current` or `remote` with how long ago it was committed.
Type to filter, and `enter` switches with a plain `git switch`; a branch only `origin` has becomes a local branch that tracks it.
In xf web, a name no branch has gets a last row, `+ make branch <name>`, whose `enter` makes it from HEAD and switches to it (`git switch -c`; spaces become dashes).
`ctrl+r` fetches `origin` first (`git fetch --prune origin`); opening the list never touches the network. xf recompiles after the switch as it does after a save, so R, I and C follow the new branch.

In xf web, `shift+enter` (or shift+click) opens the branch beside the one you are on, in a git worktree, as a project of its own, without switching or touching this checkout, so neither uncommitted changes nor a run hold it up.
The first time, xf asks the worktree's name, the branch's to start with (`enter` keeps it, typing replaces it, `esc` goes back to the list), then makes it in `.xf/worktrees/xf_<name>` at the repository's root (that folder ignores itself in git); the `xf_` marks the worktrees xf made, in `git worktree list` too.
A branch only `origin` has gets a local branch that tracks it.
A branch already checked out somewhere, which the list marks `in <folder>`, opens there instead.
The project opens here or in a new tab as `webOpenProjectIn` says, named after this one and the folder (`shop@xf_feature-x`), and the projects list and the column's projects tab show it under this project, with the project's other worktrees git knows that aren't open, dimmed (an agent session's, say), which a click opens.
Right-clicking a row in the projects tab shows a menu.
**New Worktree…** lists that project's branches: `enter` opens one in a worktree, and a name no branch has makes it, from that checkout's HEAD, in a new worktree.
**Close Project** closes an open project for every tab on it: this tab's own as `ctrl+q` does, another after asking.
**Remove Worktree** removes a worktree, and the menu also has **Open**, **Open in a New Tab** and **Copy Path**.
Opening any worktree's project links in what the main checkout has untracked and the worktree lacks: `node_modules` and `.df-credentials.json` (Dataform), `dbt_packages`, `profiles.yml` and `.user.yml` (dbt), and `.xf/config.json`.
The worktree gets its own copy of that config, kept in git's folder for the worktree, so the target section's prefix and suffixes (or dbt target), and any other setting you save in a worktree's project, stay with that worktree, and changes in the main checkout don't reach it; removing the worktree takes the copy.
On Windows folders are junctions and files copies.
`node_modules` is a folder of hard links to the main checkout's files instead, since Dataform won't load `@dataform/core` through a link.
Scratch stays per worktree.
Runs build into the same dev tables as your main checkout.
To remove a worktree, xf's or another's (never the main checkout), use `shift+del` on its row in the projects list, **Remove Worktree** in the projects tab's menu, or `> Worktree: remove this worktree` in it; all of them ask first.
If it holds uncommitted or untracked work, xf names the files and asks again before removing it anyway, losing them; the branch stays.
Closing its project (`q`) only closes it.
See [ADR 0032](adr/0032-xf-web-opens-branches-in-worktrees.md).

xf never stashes, forces or discards: when git refuses, as it does when an uncommitted change would be overwritten, the list shows git's message and your change stays.
A local run blocks a switch, since the tool reads files as it runs; an API run doesn't, as it compiles the pushed branch in GCP.
In xf web, unsaved changes in the source pane block it too: save them (`:w`) or discard them (`:e!`) first.
When your project is a folder inside a larger repository, the list says that the switch changes the whole checkout.
In the SQL pane `B` stays vim's "previous WORD", so press `1` first or use the palette there.

## Defer to prod

With defer to prod on, an upstream table that isn't built in dev is read from prod instead, as dbt's `--defer` does: you can dry run and run the models you changed without building everything upstream first.
It is xf web's (the TUI doesn't have it); turn it on or off with the palette's "Defer to prod" or by clicking it in the status line.
The switch is the project's engine's, so nvim's dry runs (`xf lsp`) follow it too, and it is remembered for the project on your machine only.
`deferToProd` (`true`) starts it on where it was never switched.

- **Dataform** has no `--defer`, so xf does what the VS Code extension does.
  Prod is the project compiled with `prodCompilerOptions` (asked for, as `I` asks, the first time you turn it on).
  For the actions on show, every upstream table that isn't built in dev and is in prod is read from prod: the SQL in pane 2, its columns and every dry run read the prod table instead.
  Before a run, xf makes a view at each such dev table that selects everything from the prod one (a *proxy view*, labelled `dataform_tools_proxy`, as the extension's are), so Dataform's own run reads prod through the dev names.
  A run asks first when it would fail (a table in neither place, no read access to prod, a dev dataset that doesn't exist: xf never makes one), when a table it reads from prod changed on your branch (build those first, run with dependencies, or run anyway), and, once per project, for an API run, whose service account needs read access to the prod datasets.
  Functions and procedures can't be views: the run builds them in dev.
  A real build of a table replaces its proxy view; "Defer to prod: remove proxy views" removes the rest, and turning defer off offers to.
- **dbt** does it itself: xf parses the merge-base with `dbtProdTarget` and gives that manifest to its compiles and to runs as `--defer --defer-state`. xf writes no views.

Pane 2 lists what the action's upstream tables are read from, and in the source pane each `ref()` is followed by `→ prod`, `✓ dev · built 3h ago` or a `⚠` that says why it can't be read from prod; hovering the label, or the `ref()` itself, shows a card that says why, names both tables and links to the one read (the `ref()`'s card shows prod's columns when prod is read), and a click on the label opens that table in BigQuery.
The run preview says how many upstream tables a run reads from prod, and the run history keeps which.

## Run from the command line, and defer to prod there

`xf run` is `dataform run` (in a dbt project, `dbt build`, as xf web runs) with the tool's own flags, as given: `xf run --actions dev_fct_orders --include-deps`, or `xf run -s +fct_orders`. xf adds what its own runs add (the name settings, or the dbt target) unless your flags give them, and keeps the run in the run history, where xf web shows it.
The tool's output goes to stdout and xf's own lines to stderr.
In Dataform, `--actions` takes names as compiled, with any table prefix, as `dataform run` does; xf says when one matches nothing.

- `--defer` defers to prod for this run alone, as described above, whatever xf web's switch says (and without changing it).
  It needs `prodCompilerOptions` or `dbtProdTarget`, and refuses flags that change the compile (`--vars`, `--schema-suffix`, `--target` …): set those in the config instead.
- What xf web asks before a run, `xf run` refuses with exit code 3, saying which flag decides it: `--defer-build-changed` builds tables read from prod that your branch changes first, in the same run (dbt too), and `--defer-allow-stale` runs all the same.
  Without `--defer`, proxy views left by an earlier run stop it too, until you pass `--defer`, `--include-deps` or `--allow-proxy-views`.
- `--dry-run` runs nothing and writes nothing: it dry runs the SQL of what the run builds (reading prod, with `--defer`) and prints the plan, as JSON with `--json`.
  A table the run builds first isn't counted as a failure.
- `--defer-remove-views` removes the project's proxy views (with a selection, those upstream of it); with `--dry-run` it lists them.

Exit codes: the tool's own for a run; 1 when a dry run failed; 3 when the run needs a decision; 4 when xf couldn't check it.
`xf run --help` lists the flags.
[`skills/xf-run`](../skills/xf-run/SKILL.md) teaches a coding agent to dry run first, show you the plan and ask before it runs; install it as [the checks skill](checks.md#agent-skill-for-cost-and-impact) is installed.

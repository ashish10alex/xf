# The TUI

From a project's root: the directory with `workflow_settings.yaml` or `dataform.json` (Dataform) or `dbt_project.yml` (dbt):

```bash
xf
```

xf looks in that directory only, not above it.
To open a project elsewhere, pass its root: `xf --dir path/to/project`.
A directory that is the root of both a Dataform and a dbt project needs `--backend dataform` or `--backend dbt`.

To try it without a project of your own, open one of the [examples](../examples): `xf --dir examples/dataform` or `xf --dir examples/dbt`.

Press `?` for every key.
The main ones are `/` to find an action, `r` to run it with the other actions its file defines (`s` first to run it alone), `d`/`D` to include their dependencies or dependents, `R` to run every action your branch changed, `T` to [run tags](running.md#run-tags), `c` to recompile, `e` for compile and dry run errors (`y` there copies them), `,` for the project's info and `q` to quit (it asks first: `q` again or `y` quits).

In `/`, a word that starts with `$` narrows the list to one kind of action, and the other words still match names: `$view orders` finds the views named like orders, and `$table $view` lists both kinds.
Dataform has `$table`, `$view`, `$incremental`, `$materializedview`, `$operation`, `$assertion`, `$notebook`, `$propertygraph` and `$declaration`; dbt has `$model`, `$table`, `$view`, `$incremental`, `$materializedview`, `$ephemeral`, `$seed`, `$snapshot`, `$test`, `$analysis`, `$operation`, `$source` and `$exposure`.
The start of a name is enough (`$inc`), and `$` alone lists them.

Start the query with `>` to list commands instead: what the keys do, by name, with the key beside each.
They are about the selected action, which the title names.
`>run` offers what `r` runs (the actions of its file, or with `selected only` on the action alone) on its own, with dependencies, dependents or both; full refresh, selected only and the Dataform API are run options, shown `on` or `off`, that apply to any of them.
The commands you do most from the palette come first, as in VS Code: by how often, then how lately, counted across projects and both the TUI and xf web (`~/.local/state/xf/commands.json`).

Quit by accident?
Run `xf` again: it reopens on the action you had selected, with the same recent actions in `/` (kept per project in `~/.local/state/xf/sessions`, or `%AppData%\xf\sessions` on Windows).

xf remembers the projects it has opened.
Run `xf` outside any project and it lists them instead of failing, most recent first, and opens the one you pick: `enter` opens it, `o` opens its folder in your editor ([settings](settings.md)), `/` filters, `x` forgets a project and `q` quits.
Flags work as usual and apply to the project you pick (`xf --nvim`).
Picking a project opens it; your shell stays in the directory it was in.
A project that has moved or been deleted is left out, and is listed again if it comes back.

To list projects before xf has opened them, let xf find them:

```bash
xf search ~/code
```

It looks in the folders you give (`--path dir` works too, and either can repeat; the working directory by default) and every folder below them, and adds each Dataform or dbt project it finds to the list, after the ones you have opened, by name and with no time.
It prints each one with its tool and path, and whether it was added or already known; projects xf already knows are left as they are.
`--backend dataform` or `--backend dbt` looks for one tool only; otherwise a folder that is both is listed as Dataform's.
It skips hidden folders, `node_modules`, `dbt_packages`, `target`, folders it can't read and the folders inside a project, and doesn't follow symlinks below the folders you give.

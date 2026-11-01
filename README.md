# xf

A lazygit-style terminal UI for [Dataform](https://github.com/dataform-co/dataform) and [dbt](https://www.getdbt.com/) projects on BigQuery, with a language server and a neovim plugin so you can edit in neovim and watch compiles, lineage, dry runs and runs in a split beside it.

> **The TUI is in maintenance mode.** New features go to [`xf web`](#in-a-browser-xf-web), the same panes in a browser. The terminal UI keeps working: it keeps building, its tests stay green and bugs that break it get fixed, but it gets nothing new. Some things below are already xf web's alone.

xf works out which tool a project uses from the files in its root and shows both the same way. Dataform projects get everything below. dbt projects get compiling, browsing, compiled SQL, lineage, compile errors, recompile on save, running (`r`), Run Changed (`R`), tag runs (`T`), the column impact (`I` in xf web, `xf impact`) and query cost (`C`, `xf cost`) of a branch, dry runs and the language server (see [dbt projects](#dbt-projects)).

```
┌ neovim ──────────────────────────┬ xf ─────────────────────┐
│ definitions/orders.sqlx          │ 1 actions   2 lineage    │
│                                  │ 0 sql       3 run  4 log │
│ save → xf recompiles            │ o → opens the action's   │
│ switch buffer → xf follows      │     file back in neovim  │
└──────────────────────────────────┴──────────────────────────┘
```

## What you need

This guide assumes you already have:

- the `dataform` CLI installed (`npm i -g @dataform/cli`) for Dataform projects, or `dbt` for dbt projects: dbt-core 1.x with the BigQuery adapter (`pip install dbt-core dbt-bigquery`) or dbt v2 (`brew install dbt-labs/dbt/dbt`),
- `gcloud` installed and logged in,
- neovim with a working config.

On top of that, xf needs:

| | Why |
|---|---|
| **macOS, Linux or Windows** | Windows 10 22H2 or 11, x64 or ARM64, in Windows Terminal or VS Code's terminal. See [Windows](#windows). |
| **neovim 0.11+** | for the language server (`vim.lsp.enable`). The `:Xf` split works on 0.10. |
| **Application Default Credentials** | dry runs, table hovers and API runs call GCP as you. `gcloud auth login` is not enough; see [step 2](#2-log-in-to-gcp-for-dry-runs). |

## 1. Install `xf`

xf is a single executable. On Windows, see [Windows](#windows).

On macOS, with [Homebrew](https://brew.sh):

```bash
brew install --cask ashish10alex/tap/xf
```

On macOS or Linux without Homebrew, the install script puts the latest release in `~/.local/bin` (`XF_INSTALL_DIR` picks another folder), after checking it against the release's checksums:

```bash
curl -fsSL https://raw.githubusercontent.com/ashish10alex/xf/main/scripts/install.sh | sh
```

With Go 1.26+ (Go 1.21+ downloads the 1.26 toolchain itself), into `$GOBIN` (by default `~/go/bin`):

```bash
go install github.com/ashish10alex/xf/cmd/xf@latest
```

Or download an archive for your OS and architecture from the [releases](https://github.com/ashish10alex/xf/releases) and put `xf` on your `PATH`.

Make sure the folder xf is in is on your `PATH`, since neovim starts `xf lsp` from it. Check it:

```bash
xf --help
```

To update later, `brew upgrade --cask xf`, run the install script again, or `go install …@latest` again. The neovim plugin warns you if the `xf` on your `PATH` is older than the plugin.

To build from source, clone the repo and run `go install ./cmd/xf` (or `just install`, with [`just`](https://github.com/casey/just)).

## 2. Log in to GCP for dry runs

xf finds credentials the way Google's client libraries do, through Application Default Credentials, which are separate from your `gcloud` login:

```bash
gcloud auth application-default login
```

You don't need to restart xf afterwards, the first time or when you log in again as another account (or run `gcloud auth application-default set-quota-project`): xf notices the credential file changed at its next Google call and uses the new login, and the dry runs and table lookups that failed are tried again. xf web says so in the status line, and looks for a new login as soon as you come back to its window. `gcloud config set project` changes none of this: xf's calls use the ADC, not the gcloud CLI's settings.

`dataform run` (the `r` key in its default `cli` mode) uses the Dataform CLI's own credentials, so the project also needs a `.df-credentials.json`. If you haven't made one, run `dataform init-creds` in the project.

## 3. Try the TUI

From a project's root: the directory with `workflow_settings.yaml` or `dataform.json` (Dataform) or `dbt_project.yml` (dbt):

```bash
xf
```

xf looks in that directory only, not above it. To open a project elsewhere, pass its root: `xf --dir path/to/project`. A directory that is the root of both a Dataform and a dbt project needs `--backend dataform` or `--backend dbt`.

To try it without a project of your own, open one of the [examples](examples): `xf --dir examples/dataform` or `xf --dir examples/dbt`.

Press `?` for every key. The main ones are `/` to find an action, `r` to run it with the other actions its file defines (`s` first to run it alone), `d`/`D` to include their dependencies or dependents, `R` to run every action your branch changed, `T` to [run tags](#run-tags), `c` to recompile, `e` for compile and dry run errors (`y` there copies them), `,` for the project's info and `q` to quit (it asks first: `q` again or `y` quits).

In `/`, a word that starts with `$` narrows the list to one kind of action, and the other words still match names: `$view orders` finds the views named like orders, and `$table $view` lists both kinds. Dataform has `$table`, `$view`, `$incremental`, `$materializedview`, `$operation`, `$assertion`, `$notebook`, `$propertygraph` and `$declaration`; dbt has `$model`, `$table`, `$view`, `$incremental`, `$materializedview`, `$ephemeral`, `$seed`, `$snapshot`, `$test`, `$analysis`, `$operation`, `$source` and `$exposure`. The start of a name is enough (`$inc`), and `$` alone lists them.

Start the query with `>` to list commands instead: what the keys do, by name, with the key beside each. They are about the selected action, which the title names. `>run` offers what `r` runs (the actions of its file, or with `selected only` on the action alone) on its own, with dependencies, dependents or both; full refresh, selected only and the Dataform API are run options, shown `on` or `off`, that apply to any of them. The commands you do most from the palette come first, as in VS Code: by how often, then how lately, counted across projects and both the TUI and xf web (`~/.local/state/xf/commands.json`).

Quit by accident? Run `xf` again: it reopens on the action you had selected, with the same recent actions in `/` (kept per project in `~/.local/state/xf/sessions`, or `%AppData%\xf\sessions` on Windows).

xf remembers the projects it has opened. Run `xf` outside any project and it lists them instead of failing, most recent first, and opens the one you pick: `enter` opens it, `o` opens its folder in your editor ([below](#optional-settings-and-api-runs)), `/` filters, `x` forgets a project and `q` quits. Flags work as usual and apply to the project you pick (`xf --nvim`). Picking a project opens it; your shell stays in the directory it was in. A project that has moved or been deleted is left out, and is listed again if it comes back.

To list projects before xf has opened them, let xf find them:

```bash
xf search ~/code
```

It looks in the folders you give (`--path dir` works too, and either can repeat; the working directory by default) and every folder below them, and adds each Dataform or dbt project it finds to the list, after the ones you have opened, by name and with no time. It prints each one with its tool and path, and whether it was added or already known; projects xf already knows are left as they are. `--backend dataform` or `--backend dbt` looks for one tool only; otherwise a folder that is both is listed as Dataform's. It skips hidden folders, `node_modules`, `dbt_packages`, `target`, folders it can't read and the folders inside a project, and doesn't follow symlinks below the folders you give.

### In a browser: `xf web`

```bash
xf web
```

opens the same panes in your browser: actions, a target section, run, SQL with its columns and lineage, the log with its jobs and history, and every overlay (`/`, `R`, `T`, `I`, `C`, `e`, `?`). The panes stack as they do in the TUI. Every key does what it does there, the SQL pane's vim keys included, but a few. `cmd+p` (`ctrl+p` on Linux and Windows) finds a file, and `#` typed in it an action; pressed again in the open palette it moves down the list, as in VS Code ([why](docs/adr/0014-xf-web-cmd-p-finds-files.md)), but in an app window on Linux and Windows, where ctrl+p moves up and ctrl+n down. `/` searches the focused pane as in vim (the SQL, zoomed or not, the columns or the source), so off a Mac ctrl+p moves up only in a box other than the palette and in an editor's completions ([why](docs/adr/0005-xf-web-finds-actions-with-cmd-p.md)). ctrl+n moves down on a Mac, but on Linux and Windows a browser tab never gets it: the browser opens a new window instead, and the arrows move. An app window does get it (below). And `tab` (`shift+tab` back) goes round pane 2's `sql`, `columns` and `lineage` tabs, and `errors` when there are any, while one of them has focus, in place of `S` ([why](docs/adr/0009-xf-web-switches-sql-and-columns-with-tab.md)); `O` shows the `lineage` tab, and again goes back to the SQL ([why](docs/adr/0022-xf-web-pane-2-has-a-target-section. The log pane's tabs (log, jobs, history and the terminal) go round with `⌥⌘→` (`⌥⌘←` back; `ctrl+alt+→/←` on Linux and Windows), from inside the terminal too, where `tab` is the shell's ([why](docs/adr/0023-xf-web-log-pane-tabs-go-round-with-cmd-option-arrows.md)).md)). And `c` goes round pane 2's `sql`, `columns` and `lineage` tabs from any pane but the source and the file tree, and gives that section the keys, so `z` zooms it and `j`/`k` move through the columns (or the lineage); it doesn't compile: `cmd+s` (`ctrl+s` on Linux and Windows) does, from anywhere, saving the source pane first ([why](docs/adr/0017-xf-web-shows-columns-with-c.md)). And `ctrl+,` (on a Mac too, where `cmd+,` is the browser's) opens and closes the settings page from anywhere, the editors included, in place of `,`. You can click, double-click and scroll as well. Drag the line between two panes to resize them; a double-click on it gives the pane its own size back, and the sizes are kept in your browser per project. It takes the same flags as `xf`; outside any project it lists the projects xf has opened before, and typing searches them by name, path or tool; `ctrl+y` copies the full path of the project (or folder) under the cursor, since `y` would type into the search.

xf web listens on 127.0.0.1 only, on a free port (`--port` picks one), and opens the URL it prints (`--no-open` just prints it). `--app`, or `webBrowser` set to `app` in your user config, opens it in an app window of Chrome, Edge or Chromium (the first found; in WSL, Windows's own, from `/mnt/c`, unless WSL can't run Windows programs, when xf web says how to fix that and tries a Linux one) instead of a tab: a window with no tabs or address bar, which gets ctrl+n (down, as in the TUI), so on Linux and Windows ctrl+n moves there as it does on a Mac. `webBrowser` can name the browser instead, by path or name (`msedge`, `/Applications/Chromium.app`); with none found, or when the browser won't start, xf web says so and opens a tab. It answers only its own page: a request must name 127.0.0.1 or localhost, and the browser's `Origin` and `Sec-Fetch-Site` headers must say it came from xf web's page, or that you opened it, so another site can't drive it. There is no token on this machine, so a reload, a restart on the same `--port`, and several xf webs at once all keep working. Runs belong to xf web, not to a tab: a run carries on when you close the tab, every open tab shows it, and reloading picks it up again. Each tab has its own selection. `esc` in pane 2 (once a zoomed pane is unzoomed) asks to close the project, and `y` (or `enter`) closes it, cancelling a local run as `q` does in the TUI, and every open tab goes back to the list of projects, where you can open another; any other key stays. `q` and `ctrl+c` don't close it. The Scratch view, the settings page and the Checks page ask the same way before going back, and their × or ← in the top bar goes at once. Closing any overlay or page (the palette, whether its pick ran or not, help, the Scratch view, the settings) gives the keys back to the source pane in vim's normal mode, unless what ran gave them to the file tree, Search or the log. In the lineage tab `enter` does nothing; a double-click jumps. To open one xf hasn't opened before, type its path in the list's search (`~/…` or `/…`): the folders it completes to are listed (tab completes, or tap a folder to go into it), each one that is the root of a Dataform or dbt project labelled with its tool, and enter opens it. Started outside any project with none opened yet, xf web starts on that list. To reach xf web from another device on your tailnet, proxy to it with `tailscale serve` and pass the Tailscale name to `--allow-host`: the URL xf web prints then carries a token, which every request needs, from this machine too. A request a proxy forwarded without `--allow-host` (one with `X-Forwarded-For` or `Forwarded`) is turned away. ctrl+c in the terminal, or in the list of projects, stops xf web. So does closing every browser window: once the last one has been closed for 45 seconds, xf web (and `xf scratch`) stops by itself, and opening the URL again before then carries on (a reload is back well within that). A run going holds it off; the 45 seconds start when the run ends. The terminal says when it is counting down and when it stops. Set `webAutoExitSeconds` in your user config, or `--web-auto-exit-seconds`, to change the wait; `0` turns it off. With `--allow-host` it is off unless one of those sets it, as a phone's browser drops the page's stream when you switch apps. `o` opens a file in the editor connected through `xf lsp`, or else with your editor setting. A terminal editor (such as `nvim` from `$EDITOR`) can't open from a browser, so `o` then lets you pick one with a window (VS Code, Cursor, Zed and so on, or any command) and saves it to your config, or to the project's `.xf/config.json` if you choose; `>` *Open: choose the editor o uses* changes it at any time.

**Back to xf web from Raycast (Mac).** An app window is a Chrome window, not an app, so Raycast's search doesn't list it. [`scripts/raycast/xf.sh`](scripts/raycast/xf.sh) is a Raycast script command that does: typing `xf` in Raycast brings the xf web window you used last to the front (the project list or a project, skipping windows whose xf web has stopped), or starts `xf web --app` when none is open. To set it up:

1. Copy it into a folder of Raycast scripts and make it executable (`chmod +x xf.sh`). In Raycast, Settings → Extensions → **+** → *Add Script Directory* adds that folder.
2. Run `xf` in Raycast once and click **Allow** when macOS asks whether Raycast may control Google Chrome. If it doesn't ask, turn on Google Chrome under Raycast in System Settings → Privacy & Security → Automation; if Raycast isn't listed there, `tccutil reset AppleEvents com.raycast.macos` makes macOS ask again.
3. Optionally, give the `xf` command a hotkey in Raycast's settings.

It looks only at Google Chrome's windows, which is what `--app` opens when Chrome is installed (it tries Chrome before Edge and Chromium). Raycast runs scripts with a bare `PATH`, so the script takes your login shell's (zsh) before starting xf web; without it, xf web can't find `dataform`. It logs what it saw and did to `~/Library/Logs/xf-raycast.log`.

**xf as a Mac app (xf web).** Chrome can install the page as an app of its own, `xf.app`, with xf's icon, in the Dock, ⌘Tab, Spotlight and Raycast. Chrome's menu, **Cast, save and share → Install page as app** (or the install icon in the address bar), does it. The app is only a window onto `http://127.0.0.1:<port>` and cannot start xf web, so it needs two things:

1. **A fixed port.** An install belongs to one address, and xf web otherwise picks a free port each run, so run it with `--port 7777` (the page you install from must be that address).
2. **An xf web that is already running.** [`scripts/launchd/xf-web-agent.sh`](scripts/launchd/xf-web-agent.sh) installs a launchd agent that keeps `xf web --port 7777 --no-open` running from your login, and starts it again if it stops:

```bash
scripts/launchd/xf-web-agent.sh install        # or: install 8080 for another port
```

Then open `http://127.0.0.1:7777` in Chrome and install the page. From then on, opening `xf` anywhere starts at once, with nothing to launch first. The agent turns the 45s auto-exit off (`--web-auto-exit-seconds 0`), takes your login shell's `PATH` so xf web finds `dataform`, `gcloud` and `dbt`, starts in your home folder (so it opens on the list of projects), and logs to `~/Library/Logs/xf-web.log`. `print` shows the plist without installing it and `uninstall` stops and removes the agent. `just install` restarts the agent on the new binary (by hand: `launchctl kickstart -k gui/$(id -u)/dev.xf.web`); re-run `install` if the path of `xf` changed. With the agent installed, a Raycast `xf` script command and the installed app both show up as `xf`; keep one of them (the app brings its own window forward, the script finds a Chrome `--app` window).

**Browser windows (xf web only).** With xf web open in more than one tab or window of a browser, the status line counts them (`⧉ 2 windows`). A click on the count, or `>` *View: browser windows*, lists them, oldest first, each with what it shows (an action, the Scratch view, the projects); `enter` or a window's number brings that one forward. Browsers seldom let a page bring another tab forward, so when yours refuses, that tab's title flashes `●` until you look at it, and the page you asked from says so. The windows find each other inside the browser alone: xf web knows nothing of what each one shows, and the windows of another browser or another device aren't counted.

**Compile errors (xf web only).** When a compile fails, the errors take the compiled SQL's place in pane 2, in an `errors` tab beside `sql (stale)` (the last good compile's) and `columns`; the page doesn't cover your work with an overlay. They are grouped as syntax / root errors (open: they usually cause the rest), unresolved references and missing dependencies, with the code a syntax error quotes, the file and line, how long the failed compile took and the binary that ran it. `e` focuses the tab, `j`/`k` move, `enter` opens a group or takes the source pane to the error's line with the editor's keys (`ctrl+o` comes back), `y` copies the errors and `tab` goes on to the stale SQL and the columns. The tab goes once the project compiles. With no compile errors, `e` shows the dry run errors as before.

**Without the Dataform CLI (xf web only).** When `dataform` isn't on your `PATH` (and no `--dataform-bin`, `$DATAFORM_BIN` or `dataformBin` names one), xf web still opens a Dataform project, so you can read and edit its files, search them and use source control; the TUI and the other commands stop and say how to install it, as before. The compile fails, so pane 2 opens on its errors tab, and above the error it shows the command that installs the CLI, `npm i -g @dataform/cli`. `enter` on that row, or its **run in terminal** button, types the command into the log pane's terminal tab, runs it and shows the tab; **copy** copies it (where there is no terminal tab, on Windows, `enter` copies it). Once the command ends and the terminal is back at its prompt, xf web finds `dataform` and compiles again by itself; after installing it elsewhere, `cmd+s` (`ctrl+s`) compiles. xf web finds the CLI on the `PATH` it started with, which a global npm install reaches when it uses the same Node: with nvm, switching the default Node to one without the CLI is a common way to lose it.

**What a compile adds to the names (xf web only).** Pane 2 has a **target** section between the actions and the SQL, open until you fold it with its caret (kept per project in your browser). In a Dataform project it has the table prefix, schema suffix and database suffix Dataform adds to the name of every table, dataset and GCP project it builds, joined with an underscore: a schema suffix `ashish` builds `xf_example` into `xf_example_ashish`. They are the settings `tablePrefix`, `schemaSuffix` and `databaseSuffix` (`--table-prefix`, `--schema-suffix`, `--database-suffix`), which xf passes to `dataform compile`, to `dataform run` and to the Dataform API's compile of an API run, so a run builds the tables the SQL names; unset, `workflow_settings.yaml`'s `namePrefix`, `datasetSuffix` and `projectSuffix` apply, as in any compile. Type in a field and press `enter` (or leave it): the value is saved, and the project reopens to compile with it, keeping the action you were on; `esc` puts the field back, and an emptied field takes the key out. The section saves to the file that already sets them, else to `.xf/config.json`, which the whole team shares once committed; to keep them in your own config, for every project, set them on the settings page's Global tab. In a dbt project the section lists the targets of the profile in `profiles.yml` (`$DBT_PROFILES_DIR`'s, the project's or `~/.dbt`'s), with the project and dataset each builds into: picking one sets `dbtTarget` and reopens the project, and picking the profile's default takes the key out. dbt has no prefix or suffix of its own; its target names the datasets. The settings page edits the same keys. See [ADR 0022](docs/adr/0022-xf-web-pane-2-has-a-target-section.md).

**A file's declarations (xf web only).** A file that declares only external tables, two or more (a `sources.js` of `declare()`s, a dbt `sources.yml`), lists them in a table in the Actions pane, in the order the file names them, with when the selected one's table was last updated (only the selected declaration is looked up; a row keeps what an earlier selection found). Moving the source pane's cursor onto a name selects its row; a click on a row puts the cursor on its name, and `j`/`k` move through the rows without moving the cursor. Any declaration or source shows its table's columns, from BigQuery, in pane 2's columns tab and opens on it; a declared view opens on its SQL instead, as BigQuery has it. A name a loop builds has no line to go to, so its row comes last, dim. The box above the rows filters them, the caret folds the table away (kept in your browser), and the row under the mouse has buttons to copy its name, open it in the BigQuery console, and preview it: a `SELECT * … LIMIT 100` in a new scratch, which waits for you to run it. See [ADR 0015](docs/adr/0015-xf-web-lists-a-files-declarations-in-a-table.md).

**Scratch queries (xf web only).** `s` on a model makes a scratch query from it: its compiled SQL, or a `SELECT` of the columns of the table it built (one to a line; the table must exist). The scratch is a file, `.xf/tmp/scratch_<id>.sql`, whose first line says the model, project and location it runs in; `.xf/tmp/` ignores itself in git. `>` *Scratch: go to last query* goes back to the scratch you last had open (else the one edited last), with the keys in its editor. The Scratch view lists your scratches beside a vim-keyed SQL editor (`ctrl+w` leaves it; `:w` saves, and it saves as you type). `ctrl+q` goes back to the project at once (it saves first), from the editor too; `esc` leaves nothing, so vim's normal mode keeps it. A click on the project's name in the top bar goes back too. Its other ctrl keys are vim's (`ctrl+v` selects a block, `ctrl+d` scrolls half a page), not the browser's or a Mac's emacs keys, and yanks, deletes and pastes share the system clipboard, as with vim's `clipboard=unnamedplus` (where the browser allows it: on localhost, not plain http). `cmd+enter` (`ctrl+enter` elsewhere) runs the selection, or the whole file (not a `ctrl+v` block), as a real BigQuery job labelled `xf-scratch`, billed to the project in the first line (else your credentials' quota project). `cmd+shift+p` (`ctrl+shift+p` elsewhere) opens the palette on Scratch's own commands, from the editor too: run or cancel, a new or deleted scratch, its project or location, *Format*, refreshing what is known of tables, the recent results, the result's CSV, its job in BigQuery, clearing filters or sort, copying a cell or row, the scratch list and results, the theme, the settings, switching project and going back. Cancelled, it gives the keys back to where they were.

While you edit, a dry run shows what the query would scan; it never blocks a run. It turns amber at 20 GiB and red at 50 GiB, where running asks first; any statement that isn't a `SELECT` (DML, DDL, scripts) asks too. Set `scratchWarnGB` and `scratchConfirmGB` in `.xf/config.json` to change the sizes. Results page in from BigQuery 500 rows at a time as you scroll; INT64, NUMERIC and BIGNUMERIC keep every digit, TIMESTAMPs show microseconds, and STRUCT, ARRAY, JSON, RANGE, GEOGRAPHY, INTERVAL and BYTES have their own cells (click one for a tree). A script shows its statements; pick one to see its rows. The `SQL` tab beside `result` shows the SQL the job ran (the selection, when a selection ran), read only, to search and copy from with vim's keys. `cmd+j` hides the results and gives the keys back to the editor, in normal mode. `ctrl+c` cancels a running job, `export CSV` streams every row of the result, and the `job` link opens it in the BigQuery console. Results are never written to disk.

**Recent results.** xf keeps the last 5 runs that returned rows, across your scratches, so you can go back to them: opening a scratch shows its latest one, and `h` (or `◷ recent` in the toolbar) lists all 5, newest first; `j`/`k` and `enter`, or a number, opens one, with its filters, sorts, search of all rows and CSV export as after a run. What is kept is each run's BigQuery job (its id, the SQL that ran, when, and how many rows), in `runs.json` beside the scratches (`.xf/tmp/runs.json`, or `tmp/runs.json` in xf's user config folder for `xf scratch` outside a project); the rows are read again from BigQuery, which keeps a query's result for about 24 hours and doesn't bill reading it. After that the result is gone, and the list marks it `expired`: run the query again. Deleting a scratch forgets its runs.

**More in the Scratch view.** Every column of the results has a filter box: type `uk` under `country` to keep the loaded rows whose country holds it (any case; several boxes combine, and nested values match their JSON). A banner says how many of the loaded rows match. While only some of the rows are loaded, a pause in typing searches every row in BigQuery, over the temporary table that holds the result (`enter` in a filter searches at once); above the same 20/50 GiB sizes it waits for `search all N rows` and asks first, and its bytes billed show. Click a column's name to sort it (ascending, descending, off; NULLs first, as BigQuery orders): the loaded rows sort at once, keeping INT64 and NUMERIC exact, and `sort all N rows` has BigQuery order every row, together with any filters, in the same query. BigQuery can't order STRUCT, ARRAY, JSON, GEOGRAPHY, INTERVAL or RANGE columns, so those don't sort. In the editor, table and column names complete as you type (`ctrl+space` asks): the project's own models first, then datasets and tables from BigQuery as you type `project.` or `dataset.`, and columns of the tables the query reads, alias-aware (`o.` lists `orders`' columns). A column several of those tables have is offered once, in the first table's spelling, with how many tables have it and their types (`INTEGER/STRING · 3 tables`); its info lists them. Every other word in the editor completes too, so a CTE's columns and aliases do once they are written; table names complete after `FROM` and `JOIN`, and in statements that name a table, such as `DROP TABLE`, `TRUNCATE TABLE`, `ALTER TABLE`, `INSERT INTO`, `UPDATE`, `DELETE` and `MERGE INTO … USING`; after `FROM` or `JOIN` the statement's CTEs come first. When BigQuery names a place in an error (`[2:29]`), from the dry run or a run, that spot is underlined in the editor and its line tinted, with the message at the end of the line, as Error Lens shows it. Hover a table name for its description, columns, partitioning, size and when it changed, with the model's own description. xf keeps what BigQuery says for 5 minutes, and forgets it when a scratch runs a CREATE, ALTER or DROP; `↻` or `:refresh` forgets it at once. The project and location a scratch runs in sit in its toolbar, and change the first line of the file.

`xf scratch` opens xf web on a new empty scratch (its file is made when you first type). In a project it works as above; anywhere else it is the editor alone, billed to `--project` and `--location`, else your credentials' quota project, and its scratches are in `tmp` in xf's user config folder (`~/.config/xf`, or `%AppData%\xf` on Windows).

**The dependency inspector (xf web only).** To follow one key (an order id, a date) through every table a model reads, and find where its rows go missing or wrong, `>` *Dependency inspector* in the palette opens a page on the selected model, as the Dataform Tools extension's Dependency Inspector does, for dbt projects too. It lists the model and what it reads, then what that reads, to the depth set at the top (3 at first, up to 20), leaving out assertions and tests; a model with no table of its own (a dbt ephemeral model, an operation with no output) gets no row, but what it reads does. Type a filter (`order_id = 'A1'`) and every row's query is `SELECT * FROM <table> WHERE <filter>`; untick *apply to all* to give each row its own. Each row dry runs (its button, or `enter` in its filter box) and runs, and *Dry run all* and *Run all* do the ticked rows. *Run all* always dry runs them first and asks, with what they scan and cost, marking any with no filter as a full scan; a single row with no filter asks too. At most 4 queries run at once, labelled `xf-inspector` and billed to the Dataform project's `defaultProject`, else your credentials' quota project; `ctrl+c` cancels them. They are xf web's, so leaving the page or reloading leaves them running. The box beside the tabs searches the tables by name or table, showing only those that have it in the rows and fading the rest in the graph; the header's tick then ticks only those (`esc` in the box clears it); *ticked only* beside it shows just the ticked rows. *+ add table* adds any other table of the project, picked as the palette picks an action, as a row marked `+` (and a node right of the model); it is kept with the model's page and its presets, and *remove* takes it off. Pasting a filter (a list of ids from a spreadsheet, say) tidies its spaces and tabs outside quotes, and so does leaving the global box; the global box wraps a long filter over up to five lines, and `shift+enter` in a filter box starts a new line. Above the result, the selected row's query shows highlighted as you type its filter, with its dry run's error in full when it fails. The result's ‹ › (or `⌥⌘←/→`, `ctrl+alt+←/→` off a Mac) go round the results of the rows that have run. `z` zooms the section you last clicked (the rows or graph, the result, the schema) to fill the page, as it zooms a pane; `z` or `esc` puts the rest back. Click a row (or a table in the `graph` tab, which `tab` shows) to see its result below, in the Scratch view's grid with its column filters; a row also opens its table's schema at the side, its query in a new scratch, its table in BigQuery or its file, and copies its query. With defer to prod on, an upstream row reads what its dry run would, prod's table when it isn't built in dev, and says `→ prod`; the model itself is always read from dev. What you typed and each row's last job are kept with the project's session, so the page comes back as you left it, reading its results again from BigQuery's copy of them (kept about a day). *Save* keeps the filters as a preset of the model, with the description beside them; *Save as new* adds another, and the preset menu loads or deletes one. Opening a model loads its latest. Leaving the page keeps it as it was, its search, dry runs and results too, until xf web quits or the tab closes: the palette's *Dependency inspector: go back to …* returns to it, a reload opens it again, and each model the page showed keeps its own. Presets are yours, not your team's: they are in `.xf/inspector/filters.json` at the main checkout's git root, which git ignores, shared by every worktree of the repository, and keyed by each action's name before dev settings, so a schema suffix, a table prefix or a dbt target doesn't lose them. `ctrl+q` goes back. See [ADR 0033](docs/adr/0033-xf-web-inspects-a-models-upstream-tables.md).

**Settings (xf web only).** The gear at the top of the page, or `,`, opens the settings page. It has two tabs, `o` and `s` switch between them, and it opens on the one it showed last. The **Overview** is the project info that `,` shows in the TUI, read-only: a line on top says how things stand (the tool and its version, the Google sign-in, the branch and what it compares with, the editor), then each section lists its labels and values, in two columns on a wide window. A click on a path copies it, and `r` reads it all again. Outside a project it is **Tools & sign-in**: the tools xf found and the Google accounts. The **Settings** are every key xf knows (those of the project's tool), grouped into Running, Tool, Editor & look and Scratch & source, with what each does. The **Global** scope edits your user config (`~/.config/xf/config.json`), the **Project** scope the project's `.xf/config.json`, which wins over it, and the **Vim** section the keys of the editor's own vim commands (in your user config: `vimNextHunk` `]g`, `vimPrevHunk` `[g`, `vimUndoHunk` `guh`, `vimStageHunk` `gsh`, `vimNextError` `]e`, `vimPrevError` `[e`, `vimHover` `K`, `vimDefinition` `gd`, `vimMenu` `gm`, `vimBufferPrev` `<leader>[`, `vimBufferNext` `<leader>]`, `vimBuffers` `<leader>b`, `vimBufferDelete` `<leader>x`, `vimFiles` `<leader>f`, `vimFormat` (no key unless you set one, e.g. `<leader>=`; see Formatting SQL), and `vimLeader` `<Space>`, which `<leader>` stands for), written as vim writes keys (`]c`, `<C-k>`), no two the same; each key says the value in use and where it comes from (a file, a flag given to xf web, or the default). A field saves on its own when you press enter, leave it or pick a choice, with the check the file's value gets, and **reset** (`x`) takes the key out of the file. The keys xf web reads as it goes (`sourceAutosaveMs`, the scratch sizes, `editor`, `theme`, `webTheme`, `webThemeLight`, `webThemeDark`, `webAutoExitSeconds`, the vim keys) apply at once, in every open tab; the rest apply when the project reopens, which a banner offers (**Reopen now**, `R`). The TUI and `xf lsp` read the same files, so a change there applies to them the next time they open the project (`theme` changes open TUIs at once). Each setting shows its field on the right and, under it, where its value comes from; a field set in no file shows the value in use, dimmed. A green bar marks a setting the file sets, with its default and **reset**. **Changed** in the sidebar (`c`) shows only those. On the list of projects the gear's Settings edit the Global file alone. See [ADR 0019](docs/adr/0019-xf-web-settings-page-has-an-overview.md).

**Editing the source (xf web only).** `E` shows the selected action's file (`.sqlx`, `.js`, or a dbt model's `.sql`) in pane `5`, beside its compiled SQL; on a phone it takes the compiled SQL's place. `enter` gives it the keys, in the same vim-keyed editor as the Scratch view, and `ctrl+w` (or `:q`) gives them back. `:w` (`cmd+s`, `ctrl+s`) saves it, keeping its line endings and byte-order mark, and the compile that follows updates the compiled SQL beside it; `:e!` drops your changes. `.sqlx` is coloured as the Dataform Tools extension colours it, with the JavaScript in `config { }`, `js { }` and `${ }` coloured as JavaScript. After a `$` it completes `${ref("…")}` from the project's actions, by the start of any part of a name or of a few of its parts in order (`$0300_ROI` finds `0300_PROMOTIONS_DATA_ROI`, as Scratch finds tables), and in a config's `dependencies` and `tags` their names (in a dbt model: refs, sources and macros), and anywhere in the SQL the columns of the tables the file refers to, each name once (after `alias.`, that table's). `tab` or `enter` takes the completion in focus, as VS Code's does (in the Scratch editor too). In a `.sqlx` file's SQL (not its `config { }`, `js { }` or `${ }`), BigQuery's functions complete below the columns, as snippets whose arguments `tab` moves through, and an argument of fixed values (`DATE_TRUNC`'s `DAY`, `WEEK`, …) offers them alone; the function's signatures show above a call as you type its arguments, with the one you are at in bold. `K` on a ref shows its table's card, on a function its signatures, what it does and a link to BigQuery's docs, and on an includes name in the JavaScript (`constants.START_DATE` in a `${ }`, `config { }`, `js { }` or a `.js` file) its declaration as `includes/constants.js` writes it (a value's whole statement, a function's signature), the comment above it and a link to its line; the mouse shows the same cards. The includes card reads that one file when asked, so it costs nothing while you type. `gd` goes to the definition of what is under the cursor, as an editor's go-to-definition with `xf lsp` does: on a ref the pane shows that action's file with the cursor on it, on an includes name (`constants.START_DATE`) the pane shows the includes file with the cursor on its declaration, and on a CTE's name the cursor moves to the CTE. `ctrl+o` goes back to where you were, from file to file, and `ctrl+i` forward again, through gd's jumps and vim's own (`G`, `gg`, `/`, `n`, `%`), one place per line as in vim; outside the editor, `backspace` goes back to the action before a gd. The files the pane has shown are its buffers, as vim's, each with a number it keeps: `<leader>]` and `<leader>[` (`:bn`, `:bp`; the leader is Space unless `vimLeader` says otherwise) go to the next and previous one, wrapping around, and `<leader>b` (`:ls`, or **Buffers: show open buffers** in the palette's `>` commands) lists them to type into and pick from, `%` marking the file shown and `#` the one before; `:b 3` or `:b orders` goes to one, and `:bd` (`<leader>x`, or `del` in the list) removes one. `<leader>f` finds a file to open, as the palette's `cmd+p` does. Going to a buffer saves unsaved changes first (a save the file's changes on disk refuse keeps you where you are), selects the action it last showed, and leaves the jump list alone; a buffer whose file is gone is dropped. The list is kept with the project's session. See [ADR 0012](docs/adr/0012-xf-web-source-pane-has-buffers.md). A definition in a file that is no action's (an include, a dbt macro) opens there too. As in vim, `gf` opens the file named under the cursor (or after it on the line), any file of the project: the name is looked for beside the file shown, then from the project's root, as written and then with `.js`, `.sqlx`, `.sql`, `.py`, `.yaml`, `.yml`, `.json` or `.md` added, so `require("includes/constants")` opens `includes/constants.js`; `gF` opens it at the line written after the name (`file.sqlx:12`), and `ctrl+o` comes back. `gc` comments lines out, or back in when they are all commented already, as vim-commentary's does: `gcc` the line, `gc` and a motion its lines (`gcj`, `gcip`), and in visual mode the selection; `.` repeats it and `u` undoes it. `cmd+/` (`ctrl+/` off a Mac) does the same to the selection's lines, as VS Code's does, in insert mode too. A `.sqlx` or `.sql` file (and the Scratch editor) comments out with `--`, a `.js` file and a `.sqlx` file's `config { }` and `js { }` with `//`, a `.py` file with `#`; lines commented with `--`, `#` or one `/* */` around them come back in. In insert mode, `ctrl+x ctrl+f` completes a file name, as vim's does: the names in the folder typed so far, from the project's root (from the file's folder after `./` or `../`), names starting with a dot only after a dot; `ctrl+n` and `ctrl+p` move through them, and picking a folder goes on into it. The last compile's errors and the file's dry run errors are marked on the lines they come from; `]e` and `[e` jump to the next and previous one, wrapping around, and show its message (in the Scratch editor too). The lines changed since git's index are marked beside the line numbers, as gitsigns marks them: a green bar for added lines, a blue one for changed lines, and a red notch where lines were taken out (a file git doesn't track yet is all added; one it ignores, or outside a repository, has no marks). The marks follow your typing, and update when you come back to the page after a `git add` or a commit. `]g` and `[g` jump to the next and previous change, wrapping around, and open its peek, as VS Code's does (a click on a mark opens it too): the lines it took out in a red block above the lines it put in, which are tinted green, with the characters that differ marked in both; it follows your typing and closes on `escape` or when the cursor leaves the change. `guh`, or the peek's **undo**, undoes the change under the cursor, putting its lines back as the index has them (`u` brings the change back; `gu` followed by any other motion still lowercases). The settings page's Vim section changes these keys, and `K` and `gd`. Only the files of the project's actions can be opened and saved. A save is refused when the file changed on disk since the pane loaded it (a save in another editor, a `git checkout`, a formatter): you then choose to reload, overwrite or see the difference. With no unsaved changes, the pane reloads by itself when the file changes. While it has unsaved changes, following the editor waits, and actions of other files can't be selected until you save or drop them. Set `sourceAutosaveMs` (in your user config or `.xf/config.json`, e.g. `1500`; less than 500 acts as 500) to save without `:w`: leaving insert mode, or the editor, or a normal-mode change (`dd`, `p`) starts the countdown, a further change restarts it, and going back into insert mode stops it, so half-typed SQL isn't compiled. Selecting another file's action, following the editor or `q` then saves first. A file changed on disk pauses autosave (the title says so) until `:w` settles it.

**The file tree (xf web only).** `⌘b` (`ctrl+b` on Linux and Windows) shows the project's files in a column on the far left, as VS Code's explorer does, from anywhere, the editors included (on Linux and Windows it takes the key from vim, whose `ctrl+b` pages up); `ctrl+0` gives it the keys. It lists what git does (tracked and untracked files, not ignored ones), marks each with its git status, its compile errors and ● for unsaved changes, and follows files made or removed outside the page. `j`/`k` move, `l` or `enter` opens a folder or a file, `h` closes a folder, `a` makes a file (end the name with `/` for a folder), `r` renames or moves, `m` moves (the same box, with the folder selected to type another; a folder that isn't there is made), `d` moves to the system's trash, `y` copies the absolute path and `Y` the path in the project, `/` filters. A click on a file opens it and leaves the keys in the tree; a double-click gives them to the editor. A right-click shows a menu: New File… on a folder (or the empty space below the files, for the project's root), Copy Path, Copy Relative Path, Rename…, Move… and Delete. Opening an action's file selects its action; any other file (an include, `workflow_settings.yaml`, a `schema.yml`) opens in the source pane on its own, with nothing selected, and `ctrl+^` goes back to the file before. Renaming an action's file renames the action when it is named after its file; the confirmation says how many actions refer to it. See [ADR 0006](docs/adr/0006-xf-web-has-a-file-tree.md).

**Source Control (xf web only).** `ctrl+g` (ctrl on a Mac too) swaps the file tree's column to Source Control, as VS Code's is; `⌘b` swaps back, and either key hides the column when its view already shows. It lists the repository's Merge Changes (files in conflict), Staged Changes, Changes, Stashes and Recent commits, with the commit message box on top (vim keys, rulers at 50 and 72, `⌘↵` commits, escape twice goes back to the list). With nothing staged, a commit asks whether to stage every change and commit (`y`), as **commit all** does. `j`/`k` move, `enter` shows a file's difference in the source pane, `space` stages or unstages (marks a conflict resolved, applies a stash), `a` stages all, `x` discards (a copy of the file goes to the trash first), `c` goes to the message box, `A` amends HEAD (not once it's pushed), `P` pushes, `p` pulls (fast-forward only; on a stash, pops it), `f` fetches, `s` stashes everything, `d` drops a stash, `n` makes a branch (at the commit under the cursor), `y` copies a commit's SHA, `/` filters. A change shows inline, the lines taken out above the lines that replaced them, in the source pane's editor, which edits the file; with `diffStyle` set to `side-by-side` (in the settings, Scratch & source) it shows the index on the left beside it. `t` (outside the editor) flips between the two, and a narrow pane shows inline. A difference opens on its first change. `gsh` stages the change under the cursor wherever the source pane shows git's changes (in a staged file's difference, it unstages it). While a push, pull, sync or fetch runs, its button spins and the status line says so. After any git command, here or in a terminal, the source pane reads its file and git's index again, so a pull shows the new text and a commit clears the change marks. A failed command shows git's last line in red, which opens the git output: every command Source Control ran. git never asks for a password here: use a credential helper or ssh-agent. See [ADR 0007](docs/adr/0007-xf-web-has-source-control.md).

**Merge requests and pipelines (xf web only).** When the branch's push remote is on GitLab or GitHub, the status line shows its merge request (pull request on GitHub) and last pipeline beside the branch: `!42 ●` while the pipeline runs, `!42 ✓` or `!42 ✗` once it ends, `+ MR` when there is none, dimmed when the host can't be reached (a VPN that is off). A click opens Source Control at its merge request region, docked at the bottom below a sash (drag it to resize, click its header to fold it; `tab` jumps between it and git's part), with the title, source → target, approvals, unresolved threads, what blocks the merge, and the pipeline's stages and jobs (a trigger job shows the status of the pipeline it started, and opens like a stage to that pipeline's stages and jobs; xf keeps asking while it runs, even once the parent has passed); `o` opens the merge request (on a job, the job), `O` the last pipeline, `m` creates one (it offers to push first, then opens the host's new merge request page), and `enter` opens the row (on a stage or a trigger job, opens or closes it). The palette has Git: create MR, open MR, open last pipeline, copy MR link and refresh MR. xf only reads, with a token of its own: add hosts and tokens in Settings › Git hosting (kept in `tokens.json` beside your config, mode 0600; `read_api` on GitLab, read access to pull requests, actions and checks on GitHub). github.com and gitlab.com need no entry, and `$GITLAB_TOKEN` / `$GITHUB_TOKEN` work there; a self-hosted host needs one. While a pipeline runs xf asks again every `forgePollSeconds` (10 by default, 5 at least), only for projects a window shows. See [ADR 0020](docs/adr/0020-xf-web-shows-the-branchs-merge-request-and-pipeline.md).

**Markdown files (xf web only).** Beside a `.md` file in the source pane, pane 2 shows the file rendered as GitHub would: tables, task lists, code blocks coloured as the SQL pane colours SQL, and the project's images. It follows your typing, unsaved changes and all. Raw HTML in the file is left out. A link to a heading scrolls there, a link to a file of the project opens it in the source pane, and a web link opens in the browser. `autoHidePane2` leaves pane 2 shown beside a Markdown file. See [ADR 0027](docs/adr/0027-xf-web-previews-markdown-in-pane-2.md).

**Python files (xf web only).** A `.py` file in the source pane is checked by [pyrefly](https://pyrefly.org)'s language server, which xf web starts the first time you open one in a project and stops when the project closes: its errors are marked as you type, `K` (or the mouse) shows its hover, `gd` goes to a definition (into a library, such as a venv's `site-packages` or the standard library, it shows the file read-only, and `ctrl+o` comes back), and completion and a call's signature show as you write. Install it with `uv tool install pyrefly` (or into the project's venv); without it, xf web says so once and the file is highlighted only. pyrefly resolves imports with the project root's `.venv/` (or `venv/`) when there is one; *Python: select interpreter* in the palette's commands, or a click on `py:` in the status line, picks another, kept for you in the project's session rather than in a file the team shares. pyrefly reports no type errors without a config, so the first time a project without a `pyrefly.toml` (or `[tool.pyrefly]` in its `pyproject.toml`) shows a Python file, xf web offers to write one at the root, leaving out `target/`, `dbt_packages/`, virtual environments and the like; `n` declines for good, and *Python: create pyrefly config* writes it later. The Settings page shows the project's config file (or a button to create one) and the interpreter under the `pythonLanguageServer` row; set to `off`, it turns it all off. See [ADR 0018](docs/adr/0018-xf-web-checks-python-with-pyrefly.md).

**Formatting SQL (xf web, and `xf lsp`).** *Format: file* in the palette's commands, `:format` (`:fmt`) in the source pane's or Scratch's editor, and *Format File* in the source pane's right-click menu, and a key if you set `vimFormat` (e.g. `<leader>=`), run [sqlfluff](https://sqlfluff.com)'s `format` over the file's SQL; `xf lsp` answers an editor's *format document* the same way. It runs only when you ask: nothing formats on save. The result lands in the editor, unsaved, as one undo step (`u` undoes it), so you read it before `:w`. In a `.sqlx` file it formats the main query and the body of each `pre_operations` and `post_operations` block, each where it sits, and the `config { }` block (in xf itself, no sqlfluff needed: one property to a line, 2-space indent, arrays on one line when they fit in 80 columns, no trailing comma, comments kept, any value that isn't a literal, array or object kept as written); `js { }`, the comments between blocks and the order of blocks stay as they were. Each `${ … }` is hidden from sqlfluff while it runs, so `${when(incremental(), …)}` in a column's place, or `${ref("x")}` inside backticks, needs no templater setting in your `.sqlfluff`. `format` runs only sqlfluff's layout and capitalisation rules, so it never changes what a query means (no added `AS`, no rewritten `GROUP BY`). Dataform only: a dbt model's Jinja isn't hidden, so xf says so and leaves it. It uses your own sqlfluff config (a `.sqlfluff`, `tox.ini`, `setup.cfg` or `pyproject.toml` in the file's folder or above it, up to the project's root), or `sqlfluffConfig` if set; with none, a style built into xf: the Dataform Tools extension's `.sqlfluff` (BigQuery, 2-space indent, leading commas, upper-case keywords and functions, aligned aliases), less the templater lines xf doesn't need, and with identifiers left as written. `sqlfluffConfig` replaces the project's own config rather than adding to it. xf never writes a config into your repo. sqlfluff is found from `sqlfluffBin`, then the project's `.venv/` or `venv/`, then `PATH` (`uv tool install sqlfluff`); without it xf says so and formats only the config block. SQL sqlfluff can't parse is left alone and the message names the line and column in your file, with no compile needed first. See [ADR 0024](docs/adr/0024-xf-formats-sql-with-sqlfluff.md).

**Search in files (xf web only).** `⇧⌘f` (also `ctrl+shift+f`; `ctrl+shift+f` on Linux and Windows) shows Search in the same column, as VS Code's is, from anywhere in the project view, the editors included, with the box focused: on the editor's selection when it has one on one line, else on the last search. `⌘b` hides it and `ctrl+g` swaps to Source Control. It searches the files the tree lists as you type, skipping binary files and files over 2 MB, and stops at 5,000 matches. `alt+c`, `alt+w` and `alt+r` toggle match case, whole word and regular expressions (RE2, with `$1` in a replacement); the `…` shows fields for files to include and exclude (`definitions/**`, `*.md, includes/`). `enter` or `↓` goes to the matches: `j`/`k` show each one in the source pane, `enter` goes there, `h`/`l` close and open a file's matches, `x` dismisses one, `/` goes back to the box. `F4` and `shift+F4` go to the next and previous match from anywhere. The `▸` beside the box shows the replace field: each match then shows what it becomes, `r` replaces the match or file under the cursor, and `⌘⌥↵` (`ctrl+alt+↵`) replaces them all after asking. Matches in the file the source pane shows change in its editor, where `u` undoes them; other files change on disk, and "undo replace" puts back the last replace's files that haven't changed since. A match whose file changed after the search is skipped, not replaced elsewhere. See [ADR 0016](docs/adr/0016-xf-web-searches-files-with-cmd-shift-f.md).

The TUI and xf web each have their own colour theme. The TUI's is `auto`, `light` or `dark`: in `auto` it follows your terminal, drawing plain text in your terminal's own text colour and picking the accents for its background. `>` *View: theme* in the TUI cycles it, saves it to the `theme` key of your user config file and changes every open TUI. xf web has themes of its own: xf Light and xf Dark (its own look), One Dark Pro, Dracula, GitHub Light, Dark and Dark Dimmed, Tokyo Night, Storm and Day, Catppuccin Latte, Frappé, Macchiato and Mocha, Night Owl and Light Owl, Nord, Ayu Light, Mirage and Dark, Monokai Pro Classic and Shades of Purple. Each one colours the whole page, not only the code, with the theme's own palette; the focused pane is the theme's green and what is under way its yellow. The `◐` button, or `>` *View: colour theme*, lists them: moving through the list shows each one, enter keeps it (saved as `webTheme`) and esc goes back. `auto` follows your browser's colour scheme: picking it goes on to ask for the theme to show while the browser is light (`webThemeLight`, xf Light by default) and then the one for dark (`webThemeDark`, xf Dark by default), each previewed as you move; esc on the way keeps what you had. `webTheme` values from before, `light` and `dark`, still mean xf's own. In a project the picker saves for every project, or for this project alone: `tab` switches, and it opens on this project when its `.xf/config.json` already sets a theme. A project's own theme (any of `webTheme`, `webThemeLight`, `webThemeDark` in its `.xf/config.json`) wins over your user config for the keys it sets, shows as `· project` on the `◐` button, and the picker's *inherit* row takes it out again. The TUI's `theme` stays yours alone. See [ADR 0021](docs/adr/0021-xf-web-theme-can-be-a-projects.md). Inside tmux, the TUI draws in true colour when your terminal supports it, as tmux reports, so your terminal's palette doesn't change xf's colours.

## 4. Use it with neovim

There are two ways to use xf with neovim.

### Quickest: `xf --nvim`

```bash
xf --nvim
```

This opens neovim in the project with xf in a split on the right, and turns on the language server for that session. You don't need to install anything in your neovim config for this. You can pass files as well: `xf --nvim definitions/orders.sqlx`.

### Permanent: install the plugin

Add the plugin to your config so that `:Xf` and the language server work in every neovim session. The plugin is the `nvim/` directory of the repo you cloned.

With [lazy.nvim](https://github.com/folke/lazy.nvim):

```lua
{
  dir = "~/path/to/xf/nvim",
  config = function()
    vim.lsp.enable("xf")
  end,
}
```

Without a plugin manager, in `init.lua`:

```lua
nvim.opt.rtp:append("~/path/to/xf/nvim")
nvim.lsp.enable("xf")
```

The plugin:

- registers the `sqlx` filetype,
- adds the `xf` language server, which gives go-to-definition on `ref()`, hovers with table details, completion and diagnostics (including dry-run errors). It attaches to `.sqlx` and JavaScript files only inside a Dataform project, and to SQL files only inside a dbt project (see [dbt projects](#dbt-projects)), so your usual JavaScript and SQL servers are left alone elsewhere.
- adds `:Xf`, which opens xf in a split beside the editor, or focuses it if it's already open.

Restart neovim, open a `.sqlx` file in a Dataform project (or a model in a dbt project), and run `:checkhealth vim.lsp` to confirm `xf` is attached.

### The loop

With xf beside neovim:

- **Save a file** and xf recompiles; its panes and your diagnostics update.
- **Switch buffers** and xf selects the action for the file you're in.
- **Press `o`** on an action in xf to open its file in the editor window.
- **Press `Z`** to toggle xf between its split and full screen.
- **`<C-w>`** moves between windows from inside the xf split, as it does elsewhere in neovim.
- **`:bnext` and `:bprevious`** skip xf, and its split keeps its buffer: a buffer command typed there fails with `E1513` instead of replacing xf.

The TUI and the language server share one background engine (`xf serve`) when they compile with the same `dataform` binary (or, for dbt, the same `dbt` binary and target). It starts when either of them needs it and stops a few seconds after the last one quits, so a save is compiled once for both. After an upgrade, the first xf of the new build to connect stops an engine of an older one and starts its own; `xf stop` stops every engine you run and lists the xf processes still running. It also saves each clean compile, so xf starts with its SQL on screen instead of waiting for `dataform compile`: while nothing it was compiled from has changed (`definitions/`, `includes/`, the settings and package files, the Dataform CLI, the day), it isn't compiled again; otherwise it shows as `outdated` while a fresh compile replaces it. Press `c` to recompile anyway. dbt compiles are saved the same way (see [dbt projects](#dbt-projects)).

## Windows

xf runs natively on Windows 10 22H2 and Windows 11, x64 and ARM64, from PowerShell or cmd. Use **Windows Terminal** or **VS Code's integrated terminal**; the old console host (conhost) isn't supported. Everything above works the same, except `xf --nvim` (see [neovim on Windows](#neovim-on-windows)).

### Install

With winget (`winget upgrade AshishAlex.xf` updates it):

```powershell
winget install AshishAlex.xf
```

With [Scoop](https://scoop.sh) (`scoop update xf` updates it):

```powershell
scoop bucket add ashish10alex https://github.com/ashish10alex/scoop-bucket
scoop install xf
```

Or with the install script, which puts the latest release in `%LocalAppData%\Programs\xf` (`$env:XF_INSTALL_DIR` picks another folder) after checking it against the release's checksums, and adds that folder to your `PATH`; run it again to update:

```powershell
irm https://raw.githubusercontent.com/ashish10alex/xf/main/scripts/install.ps1 | iex
```

Each picks the x64 or ARM64 build for your Windows. The [releases](https://github.com/ashish10alex/xf/releases) also have the zips, `xf_windows_amd64.zip` (x64) and `xf_windows_arm64.zip` (ARM64), each holding a single portable `xf.exe`. Open a new terminal (a `PATH` change applies to new ones) and check it:

```powershell
xf --help
```

To build from source instead, install Go and [`just`](https://github.com/casey/just) (`winget install Casey.Just`), clone the repo and run `just install`. It installs into `$(go env GOPATH)\bin`; add that to your `PATH` as above. Use `just install` rather than `go install ./cmd/xf` to update: Windows can't overwrite an `xf.exe` that's running (a TUI or an editor's `xf lsp`), so `just install` builds first, then moves the running one aside.

### Running a downloaded `xf.exe`

xf isn't signed. Run from a terminal, it starts without a prompt. If you double-click a downloaded `xf.exe`, or a zip extracted from a browser download, SmartScreen may say "Windows protected your PC": choose **More info**, then **Run anyway**. Or clear the download mark before extracting:

```powershell
Unblock-File .\xf_windows_amd64.zip
```

Windows Defender has in the past flagged unsigned xf builds as a trojan, a false positive. If it removes `xf.exe`, restore it from **Windows Security → Protection history**, and report the file to Microsoft as a false positive at [microsoft.com/wdsi/filesubmission](https://www.microsoft.com/wdsi/filesubmission), so the next build isn't flagged for anyone.

### Upgrading

xf's background engine runs from its own copy of xf (in `%LocalAppData%\xf\bin`), so it doesn't stop you replacing `xf.exe`. The TUI and editors' language servers run `xf.exe` itself, though, and Windows won't replace a file in use. Before upgrading, run:

```powershell
xf stop
```

It stops your engines and lists the xf processes still running, with the app each runs in (Windows Terminal, VS Code, nvim). Quit xf in those terminals and close those editor windows, then upgrade. If `winget upgrade` reports **Access is denied**, your old xf still works: run `xf stop`, close what it lists, and try again. After an upgrade, the first xf of the new build replaces any engine left from the old one.

### Tools, settings and logs

- **Dataform**: xf finds the `dataform` that `npm i -g @dataform/cli` installs (a `dataform.cmd` shim) on your `PATH`.
- **dbt**: xf uses the dbt in the project's virtualenv (`.venv\Scripts\dbt.exe` or `venv\Scripts\dbt.exe`) before the `dbt` on your `PATH`, on every OS, so a project's dbt-core is used even when another dbt (such as dbt v2) is on `PATH`. Create one with `python -m venv .venv`, then `.venv\Scripts\pip install dbt-core dbt-bigquery`. `--dbt-bin`, `$DBT_BIN` or `"dbtBin"` still win.
- **Settings**: your user config is `%AppData%\xf\config.json` (or under `$XDG_CONFIG_HOME` if you set it), and `~\` in config paths means your home folder, as `~/` does.
- **Caches and logs**: compiles and dbt artifacts are cached in `%LocalAppData%\xf`. The engine logs to `xf-<hash>-<tool>-<variant>-v<n>.log` in `%TEMP%`.
- **Long paths**: dbt and dbt v2 can fail on paths longer than Windows' 260-character limit, so keep projects at a short path, such as under `C:\src`. xf's own snapshots and caches don't need long-path support.
- **Copying the run's command**: the command line the Run pane shows pastes into PowerShell and cmd as it is.

### neovim on Windows

The language server works as on macOS: install the plugin as in [step 4](#permanent-install-the-plugin) and `vim.lsp.enable("xf")`. `xf --nvim` and the `:Xf` split aren't supported on Windows; run xf in a Windows Terminal pane beside neovim instead. It follows neovim's buffers, and `o` opens files in the neovim you used last.

### VS Code (testing only)

`editors/vscode` is a test-only VS Code extension that runs `xf lsp` in Dataform and dbt projects and opens the TUI beside the editor (**xf: Open**), where `o` opens files back in VS Code. It isn't published; build it with `just vsix target=win32-x64` (or `win32-arm64`) and install it with `code --install-extension editors/vscode/xf-win32-x64.vsix`. Disable Dataform Tools while you use it, so two language servers don't answer. See [its README](editors/vscode/README.md).

## dbt projects

Run `xf` in a dbt project's root and it compiles the project with the dbt in the project's `.venv` or `venv`, else the `dbt` on your `PATH` (or `--dbt-bin`, `$DBT_BIN`, `"dbtBin"`), dbt-core 1.x or dbt v2, and shows the same panes as for Dataform: every model by materialization (table, view, incremental, materialized view, ephemeral), seeds, snapshots, data and unit tests, analyses, sources and exposures, with their compiled SQL, columns, partitions and lineage. Saving a file recompiles. Compile errors show against the file dbt names (`e`).

- **A dbt project shows after about a second, before its compile is done.** `dbt compile` first lists the tables of every schema the project builds into, which on a large BigQuery project takes a minute or more, on every open and every save. So xf runs `dbt parse` first and shows every action and the lineage straight away, with each model's Jinja marked `compiling…`. It then compiles two things at once: the action you are looking at with the tests its Actions pane lists (one `dbt compile --select` of just those, a few seconds, or longer when the tests' schema is a large one dbt has to list too), and the whole project. Move to another action and xf compiles that one and its tests instead. The first action is the file open in your editor, else the one xf showed last, else the model you changed last (the newest file that differs from your default branch, or the newest file). After a save the saved file is compiled first and the other actions keep their last SQL, marked `outdated, compiling…`, until the whole compile lands. Dry runs and the columns pane wait for an action's compiled SQL; Run Changed (`R`) and query cost (`C`) wait for the whole compile.
- xf saves each clean dbt compile, as it does Dataform's: while nothing it was compiled from has changed (the project's `.sql`, `.yml`, `.csv`, `.py` and `.md` files outside `target/` and `logs/`, installed packages, `profiles.yml` in the project and in `~/.dbt` or `$DBT_PROFILES_DIR`, the dbt binary, the day), the next xf starts on it without running dbt at all. Each dbt binary and target keeps its own.
- dbt finds your profile as it always does (`profiles.yml` in the project, then `~/.dbt`). It compiles for `$DBT_TARGET` or else the profile's default target, unless you pick one with `--dbt-target prod` or `"dbtTarget": "prod"` in a config file. The target names the schemas (and so the actions' IDs) and anything your Jinja branches on with `target.name`; the status bar shows it when you set one. A target is picked when xf starts: restart it to switch, or in xf web pick one in pane 2's target section, which reopens the project.
- When it compiles, xf has dbt write its `target/` and `logs/` under its cache directory (`~/Library/Caches/xf/dbt` on macOS, `~/.cache/xf/dbt` on Linux, `%LocalAppData%\xf\dbt` on Windows), so compiling never touches the project's own `target/`, which your runs and `--state` comparisons use. Each dbt binary, target and `dbtCompileWithHooks` setting gets its own directory there and its own `xf serve`, so two xf on different targets, or one on dbt-core and one on dbt v2, don't share a graph.
- **dbt v2 runs `on-run-start` and `on-run-end` hooks when it compiles**, not only when it runs. So when a dbt v2 project (or a package) has hooks, xf stops at the parse and shows the graph with uncompiled SQL, marked `not compiled`, rather than run them on every save. Set `"dbtCompileWithHooks": true` in `.xf/config.json` (or `--dbt-compile-with-hooks=true`) to compile anyway. dbt-core doesn't run hooks when it compiles, so with it xf always compiles.
- An incremental model is compiled once, as dbt compiles it (as a full build when the table doesn't exist), so `v` doesn't apply.
- `r` runs the selected model, seed, snapshot or test and the rest of its Actions pane (a model's tests) with `dbt build --select`, naming each by its fqn (`xf_example.marts.fct_orders`), or with `s` the selected one alone; `d` adds `+` before each for its parents, `D` after each for its children, `f` adds `--full-refresh` (after asking) and the target you picked is passed on. The run uses dbt's own `target/` and `logs/` in the project, as a run from your terminal would, and runs the project's `on-run-start` and `on-run-end` hooks, as any `dbt build` does. The footer shows the exact command while the Run pane is focused, ready to paste into a terminal.
- A model's Actions pane lists the tests that read it too (its generic, singular and unit tests, and other models' `relationships` tests that point at it), the ones `r` runs with it; selecting a test shows the model it belongs to, and `[`/`]` move through the list. Each model, snapshot and test shows its dry run: the compiled query alone, since dbt keeps hooks only as Jinja, run in the model's own project with BigQuery picking the location. When xf could only parse a dbt v2 project, dry runs say `not compiled`; while an action's compile is still on its way they say `compiling…`.
- `R` ([Run Changed](#run-what-your-branch-changed)) lets dbt decide what changed: xf parses the commit where your branch left `main` (a git snapshot, with your installed `dbt_packages/`; a parse runs nothing in the warehouse) and lists what `dbt ls --select state:modified --state <that parse>` selects in your working tree, so a changed macro counts too. Each says why: `sql` (its file), `config`, `new`, or `upstream` (a macro or contract it depends on, or an ephemeral model it inlines: dbt lists only the ephemeral model, which can't be built, so xf lists the models that inline it). Tests show only when they changed themselves, since `dbt build` of a model runs its tests anyway. `r` builds the checked ones by fqn, with `d`, `D` and `f` as for `r`; models removed on the branch are listed, never run. There is no `a`: dbt runs your working tree. dbt's own rules apply, so some edits (such as `+tags`) don't count as changes.
- The language server works in dbt projects' `.sql` files (filetype `sql`, or the `sql.jinja`, `jinja.sql` or `dbt` some dbt plugins set): go-to-definition from `ref()` to the model's file, from `source()` to the table's entry in its YAML and from a macro call to its `{% macro %}`, as well as CTEs; hovers with the table card over `ref()`, `source()` and `{{ this }}`, and a macro's signature and documented arguments over its call; completion of names inside `ref('…')` and `source('…', '…')`, of macros in a tag, and of whole `ref()` and `source()` calls after `{{`; and an outline of a file's CTEs, references and macros. Names resolve as dbt resolves them: `ref('name')` picks the latest version unless you pass `v=`, and a name in several packages picks the file's own package, then the project's. Installed packages (`dbt_packages/`) count; dbt's built-in macros don't. Diagnostics are the compile's errors and the file's dry runs, as in a Dataform project: BigQuery's errors point into the compiled SQL, and xf traces them back by lining up the SQL between the file's Jinja tags with it, so an error lands on the line and word that made it, an error in a `{{ }}`'s output (a `ref()`, a macro) on that tag, and one in an ephemeral model's inlined CTE on the `ref()` that brought it in. Each file also gets its bytes and cost as a hint, and BigQuery's "Did you mean …?" as a quick fix. A file is dry-run only once its compile is current, never with SQL marked `compiling…` or `outdated`.
- `C` and `xf cost` ([query cost](#check-what-your-branch-would-cost)) dry run the changed models, snapshots and tests against the tables your target resolves to. dbt has no `prodCompilerOptions`: set `dbtProdTarget` (`--dbt-prod-target`) to the profile target your prod runs use, such as `prod`, and the dry runs are of the project compiled with it instead, so they read prod's tables. To show the change against the merge-base, xf runs `dbt compile` on a snapshot of that commit, once per commit and day, which takes as long as a compile of the project and connects to the warehouse as any compile does. dbt v2 isn't compiled there when the snapshot has hooks (see above), and the change column is then blank. There is no `a` or `--pushed`: dbt compares your working tree.
- Column impact ([`I` in xf web, `xf impact`](#check-what-your-branch-does-to-columns)) compares the changed models (tables, views, incremental models and materialized views) with their tables in BigQuery. With `dbtProdTarget` set to another target than the one xf compiles with, xf compiles your working tree with it and works on that compile alone: it dry runs each changed model as prod would build it, reading prod's tables (which exist where a dev target's often don't), compares it with prod's table, and asks Dataplex who reads prod's columns. Readers that are runs of xf's own target, your dev builds, fold into the models they build. The merge-base is compiled with the same target, once per commit and day, to tell `main`'s changes from your branch's. Without `dbtProdTarget`, it compares with the tables xf's target builds, and when nothing picks that target either (no `dbtTarget`, `--dbt-target` or `$DBT_TARGET`) the report says it compared with the profile's default target. It isn't asked for, as `prodCompilerOptions` is. A dbt v2 project with hooks is only parsed (see above), so it can't be checked. The TUI has no `I` in dbt projects.

## Run what your branch changed

`R` lists the actions whose compiled SQL or settings differ from where your branch left `main`, like dbt's `state:modified`, and `r` (or `enter`) runs them. Every file starts checked: `space` unchecks the file under the cursor and `x` toggles them all. Because it compares compiled output, a change in `includes/` counts too. The first `R` of the day compiles that commit in the background (about as long as a normal compile); after that, changed actions are marked with `●` in `/` and in the Actions pane, updating as you save.

It compares with `origin/main`, or your local `main` when there's no remote, and never fetches. The status line says what it compares with (`⎇ feat vs main`). To compare with another branch, press `b` in `R`, `I` or `C` (in xf web, or click the branch in R's title, the Checks page or the status line): it lists your branches, the base first, each aged by origin's copy, and `ctrl+r` fetches origin. The pick applies at once to `R`, `I` and `C`, and is saved as the project's `defaultBranch` in `.xf/config.json` (taken out again when it matches your own config or `main`); another xf open on the project picks it up when it next starts. With `a` on (API runs), it compares what you pushed, since that is what Dataform runs.

A renamed file whose action takes its name from the file gives the action a new name, so the table it builds is a new one. When git calls the file a rename (`git diff -M`, as GitLab and GitHub do, so a file that was also edited counts as long as most of it is the same), the new action is marked `renamed`. Its file shows the old one, such as `← 0200_OLD_NAME.sqlx (95%)`, and the old action isn't listed again under the removed ones. This works for a rename that isn't staged yet too: xf marks the new file in a copy of the index, never in yours. A file that declares several actions isn't paired, and neither are the assertions generated from a config.

## Run tags

`T` lists the project's tags, each with how many actions it would run. It is a search box: type to filter the tags, `space` checks the one under the cursor (the search stays, so you can check several matches) and `enter` runs every action that has any of the checked tags. Nothing starts checked and nothing runs until you check a tag; a tag the search hides stays checked, and the checked tags are named under the list.

The run uses the Run pane's options (`d`, `D`, `f`, and `a` in a Dataform project), shown under the list. Every letter is the search, so `esc` leaves it for the options: there `d`, `D`, `f` and `a` toggle as in the Run pane, `space`, `enter` and `j`/`k` still work, `/` goes back to the search and `esc` again closes. A full refresh asks first, as `r` does. The tool selects the actions, so the command stays short: `dataform run --tags daily hourly`, `includedTags` for an API run, and `dbt build --select tag:daily tag:hourly`. An API run compiles the pushed branch, so a tag you haven't pushed isn't in it, though xf counts it. `> Run tags` in the palette opens the same overlay.

## Switch branch

The status line names the branch your project's checkout is on, and how it differs from `origin/<branch>` as of your last fetch: `⎇ main ●3 ↑2 ↓1` is 3 uncommitted files, 2 commits you haven't pushed and 1 you haven't pulled, each shown only when it isn't zero. A detached HEAD shows `⎇ (detached abc1234)`, and outside a git repository there is nothing. It follows a switch, commit or fetch you make in a terminal at once, and in xf web hovering it lists the files and commits it counts. The projects list (`xf` or `xf web` started outside a project) shows each project's branch too.

`B` (or `> Git: switch branch` or `> Git: checkout branch`, or a click on the branch in xf web) lists your local branches and those only `origin` has, newest commit first, each marked `current` or `remote` with how long ago it was committed. Type to filter, and `enter` switches with a plain `git switch`; a branch only `origin` has becomes a local branch that tracks it. In xf web, a name no branch has gets a last row, `+ make branch <name>`, whose `enter` makes it from HEAD and switches to it (`git switch -c`; spaces become dashes). `ctrl+r` fetches `origin` first (`git fetch --prune origin`); opening the list never touches the network. xf recompiles after the switch as it does after a save, so R, I and C follow the new branch.

In xf web, `shift+enter` (or shift+click) opens the branch beside the one you are on, in a git worktree, as a project of its own, without switching or touching this checkout, so neither uncommitted changes nor a run hold it up. The first time, xf makes the worktree in `.xf/worktrees/<branch>` at the repository's root (that folder ignores itself in git); a branch only `origin` has gets a local branch that tracks it. A branch already checked out somewhere, which the list marks `in <folder>`, opens there instead. The project opens here or in a new tab as `webOpenProjectIn` says, named after this one and the folder (`shop@feature-x`), and the projects list and the column's projects tab show it under this project, with the project's other worktrees git knows that aren't open, dimmed (an agent session's, say), which a click opens. Right-clicking a row in the projects tab shows a menu. **New Worktree…** lists that project's branches: `enter` opens one in a worktree, and a name no branch has makes it, from that checkout's HEAD, in a new worktree. **Remove Worktree** removes a worktree, and the menu also has **Open**, **Open in a New Tab** and **Copy Path**. Opening any worktree's project links in what the main checkout has untracked and the worktree lacks: `node_modules` and `.df-credentials.json` (Dataform), `dbt_packages`, `profiles.yml` and `.user.yml` (dbt), and `.xf/config.json`, so settings are shared; on Windows folders are junctions and files copies. `node_modules` is a folder of hard links to the main checkout's files instead, since Dataform won't load `@dataform/core` through a link. Scratch stays per worktree. Runs build into the same dev tables as your main checkout. To remove a worktree, xf's or another's (never the main checkout), use `shift+del` on its row in the projects list, **Remove Worktree** in the projects tab's menu, or `> Worktree: remove this worktree` in it; all of them ask first. If it holds uncommitted or untracked work, xf names the files and asks again before removing it anyway, losing them; the branch stays. Closing its project (`q`) only closes it. See [ADR 0032](docs/adr/0032-xf-web-opens-branches-in-worktrees.md).

xf never stashes, forces or discards: when git refuses, as it does when an uncommitted change would be overwritten, the list shows git's message and your change stays. A local run blocks a switch, since the tool reads files as it runs; an API run doesn't, as it compiles the pushed branch in GCP. In xf web, unsaved changes in the source pane block it too: save them (`:w`) or discard them (`:e!`) first. When your project is a folder inside a larger repository, the list says that the switch changes the whole checkout. In the SQL pane `B` stays vim's "previous WORD", so press `1` first or use the palette there.

## Check what your branch does to columns

`I` shows the column impact of the same changed actions, as the Markdown report a pull request gets; `y` copies it. Every changed table, view and incremental table is dry run and compared with its table in BigQuery, and Dataplex Data Lineage is asked who reads the columns it drops or retypes, and the tables it deletes.

The report is a ledger. Its heading is the verdict ("✅ Nothing to fix", or "🚨 2 to fix before merging"), then there is one row per model: what it changes (all the columns it drops, retypes or adds, in one row) and who that affects, each reader with how it reads (exact copy, derived, may read) and what this branch does about it. Readers still to fix are in bold. A rename is a single row, `old → new`. When every model is in one dataset, the dataset is named once in the heading and the rows leave it out. Rows are numbered down the left, and a collapsed "Every reader" table follows, each reader citing its row by that number and the table it reads there (`1 · orders`; the old name, for a rename). On the `--web` page the number is a link to its row. The numbers are positions in that report: another check may number the same model differently. They are bare digits, since `#1` would link to issue 1 on GitHub and GitLab. Each row is marked: 🚨 a column or table something still reads is gone, ⚠️ a column something reads changes type, ❔ not checked or its readers couldn't be looked up, ℹ️ changed but nothing reads it any more, 🔁 renamed, 🆕 new, ✅ keeps every column. The terminal draws the same ledger, with words for the marks.

A reader this branch changes that still names a dropped column may only name it: as an alias of a new column, say. So xf dry runs it, with the changed table swapped for the query the branch builds it with, and lets BigQuery resolve the names: when the dry run passes the reader no longer reads the column ("no longer reads it (dry run passes)"), and when BigQuery can't find the column it does ("still reads it (dry run fails)").

An action that is new on the branch is marked 🆕: with no table in BigQuery yet there is nothing to lose, so it isn't dry run ("new: nothing reads it yet"). When a table is already there, because someone ran the branch or an action deleted earlier is back, it is still compared ("new, its table is already there"), and marked as any changed table is.

A renamed action (see [Run Changed](#run-what-your-branch-changed)) has no table under its new name until it runs, so it is compared with the table it was renamed from, in one 🔁 row, `old → new`. Its old table's readers are listed too: Dataform doesn't drop it, so a reader still on the old name keeps reading a table that nothing updates any more. A reader this branch moved onto the new name shows as "moved to the new name". And where the renamed action is itself a reader of another changed table, it is judged by its new SQL under its new name ("renamed here"), as any reader the branch changes is.

A change your base branch already made isn't held against your branch. When the table compared with is behind the base branch, say a column was dropped on `main` in a commit that isn't released yet, every later branch that touches the model would show that drop as its own. So when a table drops or retypes a column, xf also dry runs the action as it is at the merge-base, which is already compiled: a change it makes too is listed as "already changed on `origin/main`", counts as low whoever reads the column, and never fails `--fail-on`. The readers are still listed, since the next release breaks them all the same. When the merge-base's version can't be dry run, the change stays your branch's.

The report started as the extension's "Column Impact of Branch Changes" and now goes its own way in layout; which readers it finds, and how it ranks them, still match the extension.

Dataplex also records lineage from dev runs, so a reader may be your own dev build of a project table. To fold those into the actions they run, as the extension does, set `devCompilerOptions` to the compiler options you develop with, in your personal `~/.config/xf/config.json` (or `--dev-compiler-options`):

```json
{ "devCompilerOptions": "--schema-suffix=ashish" }
```

xf then compiles the project once more with them when it checks. Without it, dev-run readers show as readers outside the project.

By default the tables it compares with are your actions' default targets, from `workflow_settings.yaml` with no compiler options. If those are dev tables, and prod runs reach prod through compiler options (a release configuration's `--default-database`, `--vars` and so on), set `prodCompilerOptions` to those options in the project's `.xf/config.json` (or `--prod-compiler-options`), so the whole team compares with prod:

```json
{ "prodCompilerOptions": "--default-database=acme-prod --vars=executionSetting=prod" }
```

xf then compiles the project once more with them, matches each action to its table there by dataset and name, and compares with that table and looks up its readers. The report says which project it compared with (`Evaluated against: tables in acme-prod`). Without it, a table you've already built from the branch in your default project compares as unchanged. Readers come from prod runs in the last 30 days, about two hours behind. Looking them up needs the Data Lineage API enabled and `roles/datalineage.viewer`.

So xf asks. The first `I` or `C` in a project where nothing sets `prodCompilerOptions` opens a prompt for them. `enter` compiles the project with what you typed and, when that works, saves it to `.xf/config.json`, keeping the file's other keys; a value the project doesn't compile with stays in the box with the error. If your default targets are prod already, `tab` to "the default targets are prod" and `enter`: that saves `"prodCompilerOptions": ""`, which is an answer too. `esc` checks against the default targets and asks again the next time xf starts. Commit the file and nobody on the team is asked again.

Until it is set, in a file or with the flag, every report says so: under "against" in the TUI, on stderr from `xf impact`, and as the last line of the Markdown:

```
Compared with the project's default targets (`acme-dev`), which may be dev tables. Set `prodCompilerOptions` to compare with prod.
```

The check runs in the background engine: close the overlay and it carries on; open it again and the report is there, flagged when you've changed the branch since, until `c` checks again. With `a` on, it checks what you pushed.

`xf impact` prints the same report from the command line, for scripts and CI; see [below](#column-impact-in-cicd).

## Defer to prod

With defer to prod on, an upstream table that isn't built in dev is read from prod instead, as dbt's `--defer` does: you can dry run and run the models you changed without building everything upstream first. It is xf web's (the TUI doesn't have it); turn it on or off with the palette's "Defer to prod" or by clicking it in the status line. The switch is the project's engine's, so nvim's dry runs (`xf lsp`) follow it too, and it is remembered for the project on your machine only. `deferToProd` (`true`) starts it on where it was never switched.

- **Dataform** has no `--defer`, so xf does what the VS Code extension does. Prod is the project compiled with `prodCompilerOptions` (asked for, as `I` asks, the first time you turn it on). For the actions on show, every upstream table that isn't built in dev and is in prod is read from prod: the SQL in pane 2, its columns and every dry run read the prod table instead. Before a run, xf makes a view at each such dev table that selects everything from the prod one (a *proxy view*, labelled `dataform_tools_proxy`, as the extension's are), so Dataform's own run reads prod through the dev names. A run asks first when it would fail (a table in neither place, no read access to prod, a dev dataset that doesn't exist: xf never makes one), when a table it reads from prod changed on your branch (build those first, run with dependencies, or run anyway), and, once per project, for an API run, whose service account needs read access to the prod datasets. Functions and procedures can't be views: the run builds them in dev. A real build of a table replaces its proxy view; "Defer to prod: remove proxy views" removes the rest, and turning defer off offers to.
- **dbt** does it itself: xf parses the merge-base with `dbtProdTarget` and gives that manifest to its compiles and to runs as `--defer --defer-state`. xf writes no views.

Pane 2 lists what the action's upstream tables are read from, and in the source pane each `ref()` is followed by `→ prod`, `✓ dev · built 3h ago` or a `⚠` that says why it can't be read from prod; hovering the label, or the `ref()` itself, shows a card that says why, names both tables and links to the one read (the `ref()`'s card shows prod's columns when prod is read), and a click on the label opens that table in BigQuery. The run preview says how many upstream tables a run reads from prod, and the run history keeps which.

## Check what your branch would cost

`C` shows what the same changed actions would scan in BigQuery, and what that costs on demand; `y` copies the report as the Markdown a pull request gets. Every changed action with a query is dry run: tables, views, incremental tables, assertions and operations (in dbt: models, snapshots and tests). Dry runs are free and run nothing. The report lists them with the most bytes first:

- **Scans and cost.** BigQuery's estimate, in binary units as it bills (1 GiB is 2³⁰ bytes), priced at $6.25 per TiB. `≤` marks an upper bound, which is what BigQuery gives for clustered tables, and `≥` a lower bound; the total carries the mark too.
- **Incremental tables** count their incremental run, the one that recurs, with the full rebuild beside it.
- **Change** is against the same action at the commit where your branch left `main`, dry run now, so both sides read today's tables: `+1.20 GiB`, `no change`, or `new`. The branch's own figures show first and the change fills in after, since it needs the merge-base compiled.
- **⚠ no partition filter** marks a query that reads a partitioned table by name and never names its partition column (`_PARTITIONTIME` or `_PARTITIONDATE` for a table partitioned by ingestion time), so nothing can be pruned. It is a check of the compiled SQL's text: a column that is selected but not filtered on passes, and a table read through a view isn't judged, since the view may filter it. A table created with `require_partition_filter` fails its dry run instead, and shows under "Not estimated" with BigQuery's message.
- **Not estimated** lists the actions BigQuery gave no figure for, with the reason: a query that doesn't compile against the tables as they are, or a script (an operation, or a table with `pre_operations`), which BigQuery rarely estimates. They aren't in the total.

In a Dataform project with `prodCompilerOptions` set, the dry runs are of the project compiled with those options, so the bytes are those of the tables prod runs read, not of your dev tables. An action that needs a column another changed action adds can't be planned against prod until that one has run there, and is listed as not estimated.

Without it, `C` asks for it first, as `I` does (see [above](#check-what-your-branch-does-to-columns)), and until it is set the report says the dry runs read the project's default targets, which may be dev tables: in the TUI, in `xf cost`'s text and Markdown, and as `"unsetSetting": "prodCompilerOptions"` in its JSON. A dbt project says the same of its target when nothing chooses one (no `dbtProdTarget`, `dbtTarget`, `--dbt-target` or `$DBT_TARGET`), with `"unsetSetting": "dbtProdTarget"`; it isn't asked for.

The check runs in the background engine, as `I` does: close the overlay and it carries on; open it again and the report is there, flagged when you've changed the branch since, until `c` checks again. With `a` on in a Dataform project, it estimates what you pushed.

`xf cost` prints the same report from the command line, and can fail a CI job over a limit; see [below](#query-cost-in-cicd).

## Column impact in CI/CD

`xf impact` checks a pull request's column impact and prints the report as Markdown on stdout (or writes it to a file with `--output`), ready to post as a PR comment, with progress on stderr. `--json` prints it as JSON instead, for scripts (see [below](#impact-json)). It always checks afresh. Run from inside the project, it:

1. compiles the checked-out project, and the merge-base with `origin/main`, with the `dataform` CLI (or `dbt`, with `dbtProdTarget` when it is set),
2. dry runs each changed table, view and incremental table (in dbt: model), and reads its table in BigQuery,
3. asks Dataplex who reads the columns it drops or retypes, and the tables it deletes.

It starts a `xf serve` in the background to do the work, which stops a few seconds after `xf impact` exits; there is nothing to start or clean up.

On your own machine, `xf impact --web` opens xf web on the column impact tab of its Checks page, the page `I` (or **Check: column impact of changes** in the palette) opens: the check runs there with its progress, `y` copies the Markdown, `c` checks again, and xf web runs until ctrl+c as `xf web` does. It can't be used with `--pushed`, `--fail-on`, `--output` or `--json`.

| Exit code | Meaning |
|---|---|
| `0` | The report was printed, and nothing reached `--fail-on`. |
| `1` | The report was printed, and `--fail-on critical` found a 🚨 change (or `--fail-on warning` a 🚨 or ⚠️ one). |
| `2` | The check couldn't be made: compile errors, no merge-base, no credentials, and so on. The reason is on stderr. |

A change whose readers couldn't be looked up (❔) never trips `--fail-on`; `xf impact` says so on stderr.

| Flag | |
|---|---|
| `--fail-on critical\|warning` | exit 1 at that severity or worse |
| `--output path` | write the report to `path` instead of stdout |
| `--json` | print (or write) the report as JSON instead of Markdown |
| `--web` | open xf web on its Checks page, where the check runs as `I` runs it, instead of printing the report |
| `--default-branch name` | compare with `origin/<name>` rather than `main` (or set `defaultBranch` in `.xf/config.json`) |
| `--pushed` | check the branch pushed to origin instead of the checked-out files (Dataform only) |
| `--dataform-bin path` | the `dataform` binary to compile with |
| `--dbt-bin path`, `--dbt-target name` | the `dbt` binary, and the target xf compiles with |
| `--dbt-prod-target name` | compile a dbt project with this target and compare with the tables it builds (or set `dbtProdTarget`) |
| `--dev-compiler-options options` | fold readers that are dev runs built with these options into their actions (usually unset in CI) |

### Impact JSON

`--json` prints one document, and `--output impact.json` writes it to a file instead. `version` is 1: fields may be added, and it changes if one is removed or changes meaning. `worst` is the most urgent change's severity, as `--fail-on` ranks them, left out when nothing is at risk. `atRisk` holds the tables with dropped or retyped columns, or deleted, each with its `columns` and their `readers`; each table and column has a `severity`, so a script or agent can pick out what to fix without working out the rules:

| `severity` | Means |
|---|---|
| `critical` | 🚨 a dropped column or deleted table that something still reads |
| `warning` | ⚠️ a retyped column that something still reads |
| `unknown` | ❔ its readers couldn't be looked up |
| `low` | ℹ️ nothing still reads it: no readers, only ones this branch probably updated, or the base branch made the change |

A table's `severity` is its most urgent column's, an `unknown` counting above a `warning` as the report's marks do. A reader's `filePath` is set when it is an action of this project, and `changedOnBranch`, `probablyUpdated`, `stillNamed` and `deletedOnBranch` say what this branch does to it. `safe`, `new` and `unchecked` are the rest of the report.

```json
{
  "version": 1,
  "worst": "critical",
  "comparison": {"headRef": "feat/orders", "baseRef": "origin/main", "mergeBase": "1125280…", "headLabel": "working tree"},
  "changedCount": 2,
  "safe": [{"table": "acme.marts.daily", "type": "view", "columns": []}],
  "new": [],
  "unchecked": [],
  "against": ["acme"],
  "checkedAt": "2026-10-31T19:52:04Z",
  "signature": "7cfba74…",
  "atRisk": [
    {
      "table": "acme.marts.orders",
      "fileName": "definitions/marts/orders.sqlx",
      "type": "table",
      "severity": "critical",
      "columns": [
        {
          "column": "discount",
          "change": {"kind": "dropped"},
          "readers": [{"table": "acme.finance.margins", "column": "discount", "dependencyType": "EXACT_COPY", "filePath": "definitions/finance/margins.sqlx"}],
          "severity": "critical"
        }
      ]
    }
  ]
}
```

### What the CI job needs

- **A Linux or macOS runner** with **Go 1.26+** to install xf, and **Node** for the `dataform` CLI (`npm i -g @dataform/cli`; run `npm ci` too if the project has a `package.json`).
- **The default branch's history.** xf never fetches: it needs `origin/main` and enough history to find the merge-base, so fetch everything (`fetch-depth: 0` in GitHub Actions) or at least `git fetch origin main`.
- **The PR's branch checked out**, so the report names it. A detached checkout still works, but the report shows a commit instead.
- **Google credentials** through Application Default Credentials: Workload Identity Federation, or a service account key in `GOOGLE_APPLICATION_CREDENTIALS`. The identity needs:

  | Role | On | For |
  |---|---|---|
  | `roles/bigquery.jobUser` | the project running dry runs | running the dry runs |
  | `roles/bigquery.dataViewer` | the dataset of each changed table | reading their current schemas |
  | `roles/datalineage.viewer` | each changed table's project | asking Dataplex for readers |

  With user credentials rather than a service account, the quota project (`gcloud auth application-default set-quota-project`) needs the Data Lineage API enabled too.

### GitHub Actions example

Add this to `.github/workflows/impact.yml`:

```yaml
name: Column impact

on:
  pull_request:
    types: [opened, synchronize, reopened]

jobs:
  impact:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write          # for workload identity federation
      pull-requests: write     # for sticky-pull-request-comment
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0       # xf needs history to find the merge-base

      - uses: actions/setup-node@v4
        with:
          node-version: "20"
      - run: npm i -g @dataform/cli
      # - run: npm ci  # if the project has a package.json

      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
          cache: false
      - run: go install github.com/ashish10alex/xf/cmd/xf@<tag or commit>

      - uses: google-github-actions/auth@v2
        with:
          workload_identity_provider: projects/123456789/locations/global/workloadIdentityPools/github/providers/github
          service_account: dataform-ci@my-project.iam.gserviceaccount.com

      - name: Check column impact
        id: impact
        run: |
          set +e
          xf impact --fail-on critical --output impact.md
          code=$?
          echo "code=$code" >> "$GITHUB_OUTPUT"
          [ "$code" -eq 2 ] && exit 2   # the check couldn't be made
          cat impact.md >> "$GITHUB_STEP_SUMMARY"
          exit 0

      - uses: marocchino/sticky-pull-request-comment@v2
        with:
          header: column-impact
          path: ${{ github.workspace }}/impact.md  # adjust if working-directory isn't the root

      - name: Fail on critical changes
        if: steps.impact.outputs.code == '1'\
        run: |\
          echo "::error::This PR drops columns or deletes tables that are still read. See the column impact comment."
          exit 1

      - name: Show the engine's log
        if: failure()
        run: cat "${TMPDIR:-/tmp}"/xf-*.log || true
```

Pull requests from forks get no `id-token` or secrets, so the check can't log in there. Run it on branches in the repository.

### Other CI systems

The same steps work anywhere a shell does. In GitLab CI, Cloud Build, Jenkins and the like:

```bash
git fetch origin main                     # xf needs origin/main; it never fetches
git checkout -B "$BRANCH" "$COMMIT"       # optional: the report names the branch
npm i -g @dataform/cli
go install github.com/ashish10alex/xf/cmd/xf@<tag or commit>
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/key.json  # or workload identity

set +e
xf impact --fail-on critical --output impact.md
code=$?
set -e
[ "$code" -eq 2 ] && exit 2
# post impact.md to the merge request, e.g. with glab mr note
exit "$code"
```

If your default branch isn't `main`, add `--default-branch <name>`, or commit `defaultBranch` in `.xf/config.json` so the TUI and CI agree. When a run fails, the engine's log is `xf-<hash>-<tool>-<variant>-v<n>.log` in `$TMPDIR` (or `/tmp`; `%TEMP%` on Windows).

## Query cost in CI/CD

`xf cost` estimates what a pull request's changed actions would scan and prints a line for each, with progress on stderr. It works in Dataform and dbt projects, always checks afresh, and starts and stops its own `xf serve` as `xf impact` does.

```text
$ xf cost --fail-over 100GB
4 changed actions in feature/v2-marts vs origin/main @ 1125280 (working tree)
⚠ marts.fct_order_items  ≤ 4.20 TiB  $26.25  +4.20 TiB  (full refresh 9.00 TiB)  [MISSING PARTITION FILTER: event_date on raw.events]
✓ marts.dim_customers      1.80 GiB  $0.011  new
✓ staging.stg_pageviews  420.00 MiB  $0.003  no change
? ops.audit_log          not estimated: BigQuery gives no estimate for a script
────────────────────────────────────────────────────────────
Total scanned: ≤ 4.20 TiB | Estimated cost: ≤ $26.26
✗ Gate failed: 4.20 TiB exceeds the limit of 100.00 GiB
```

What each column means is under [Check what your branch would cost](#check-what-your-branch-would-cost).

| Exit code | Meaning |
|---|---|
| `0` | The report was printed, and the total is within `--fail-over` (or there is no limit, or nothing to estimate). |
| `1` | The report was printed, and the total is over `--fail-over`. |
| `2` | The check couldn't be made: compile errors, no merge-base, no credentials, BigQuery refused every dry run, and so on. The reason is on stderr. |

The limit is on the total bytes, not the dollars, so the price never decides a build. An action that couldn't be estimated is left out of the total and never trips the limit; `xf cost` says how many there were on stderr. A missing partition filter is a warning only. If the merge-base can't be compiled, the report has no change column, stderr says why, and the exit code is the same as with one.

| Flag | |
|---|---|
| `--fail-over size` | exit 1 when the changed actions scan more than `size` between them: `100GB`, `1.5TiB`, `500MB`. Units are binary with or without the `i` |
| `--usd-per-tib price` | price a TiB at `price` dollars instead of 6.25, for another region's on-demand rate |
| `--output path` | also write the report to `path`: as Markdown, for a PR comment, or as JSON with `--json` |
| `--json` | print JSON on stdout instead of text |
| `--color auto\|always\|never` | colour the text. `auto`, the default, colours a terminal and leaves a pipe, a file or a CI log plain; it also honours `NO_COLOR`. `always` is for CI systems that render ANSI colour |
| `--default-branch name` | compare with `origin/<name>` rather than `main` (or set `defaultBranch` in `.xf/config.json`) |
| `--pushed` | estimate the branch pushed to origin instead of the checked-out files (Dataform only) |
| `--prod-compiler-options options` | dry run the Dataform project compiled with these options (or set `prodCompilerOptions`) |
| `--dataform-bin path`, `--dbt-bin path`, `--dbt-target name` | the tool to compile with, and dbt's target |
| `--dbt-prod-target name` | dry run the dbt project compiled with this target (or set `dbtProdTarget`) |

The job needs what [the column impact job needs](#what-the-ci-job-needs), less Dataplex: `roles/bigquery.jobUser` on the project the dry runs run in, and `roles/bigquery.dataViewer` on the datasets the changed actions read. For dbt, install dbt and its packages (`dbt deps`) instead of the `dataform` CLI.

### JSON

`--json` prints one document. `version` is 1: fields may be added, and it changes if one is removed or changes meaning.

```json
{
  "version": 1,
  "comparison": {"headRef": "feature/v2-marts", "baseRef": "origin/main", "mergeBase": "1125280…", "headLabel": "working tree"},
  "compiledWith": "prodCompilerOptions",
  "dryRunIn": "acme-prod (EU)",
  "actions": [
    {
      "name": "acme.marts.fct_order_items",
      "dryRunAs": "acme-prod.marts.fct_order_items",
      "file": "definitions/marts/fct_order_items.sqlx",
      "type": "incremental",
      "bytes": 4617948836659,
      "accuracy": "UPPER_BOUND",
      "usd": 26.25,
      "fullRefresh": {"bytes": 9895604649984, "usd": 56.25},
      "new": false,
      "base": {"bytes": 3221225472, "accuracy": "PRECISE", "usd": 0.018},
      "deltaBytes": 4614727611187,
      "warnings": [{"kind": "missing_partition_filter", "table": "acme.raw.events", "column": "event_date"}]
    },
    {"name": "acme.ops.audit_log", "type": "operations", "bytes": null, "usd": null, "new": false, "deltaBytes": null, "warnings": [], "error": "BigQuery gives no estimate for a script"}
  ],
  "totalBytes": 4620332507955,
  "totalBound": "upper",
  "totalUsd": 26.26,
  "baseTotalBytes": 3661627392,
  "deltaTotalBytes": 4616670880563,
  "usdPerTib": 6.25,
  "unestimated": 1,
  "gate": {"thresholdBytes": 107374182400, "exceeded": true},
  "checkedAt": "2026-10-06T09:30:00Z"
}
```

`bytes`, `usd` and `deltaBytes` are `null` when there is no figure, never 0; `accuracy` is BigQuery's (`PRECISE`, `UPPER_BOUND`, `LOWER_BOUND`, `UNKNOWN`); `totalBound` is `lower` or `upper` when the total is a bound; `gate` is there with `--fail-over`; `baseError` says why no action has a `base`; `unsetSetting` names the setting that would dry run against prod's tables (`prodCompilerOptions`, or `dbtProdTarget`) when nothing sets it.

`baseTotalBytes` is what the same actions scanned at the merge-base, a new action counting nothing, and `deltaTotalBytes` how many more the branch scans; both are `null` when the merge-base couldn't be compiled or an action other than a new one has no `base`. `dryRunIn` is the project the dry runs ran in, with their location (left out when they ran in more than one), and an action's `dryRunAs` is the table they read for it when that isn't its `name` (prod's, with `prodCompilerOptions`). When an incremental table's first build is in the total, `laterBytes`, `laterUsd` and `laterBound` give each run after the first, with its incremental run instead.

### GitHub Actions example

The setup steps are those of the [column impact workflow](#github-actions-example); the check itself is:

```yaml
      - name: Check query cost
        id: cost
        run: |
          set +e
          xf cost --fail-over 100GB --output cost.md
          code=$?
          echo "code=$code" >> "$GITHUB_OUTPUT"
          [ "$code" -eq 2 ] && exit 2   # the check couldn't be made
          cat cost.md >> "$GITHUB_STEP_SUMMARY"
          exit 0

      - uses: marocchino/sticky-pull-request-comment@v2
        with:
          header: query-cost
          path: ${{ github.workspace }}/cost.md  # adjust if working-directory isn't the root

      - name: Fail over the limit
        if: steps.cost.outputs.code == '1'
        run: |
          echo "::error::This PR's changed actions scan more than 100 GB. See the query cost comment."
          exit 1
```

Run it after `xf impact` in the same job and the merge-base is compiled once for both.

## Agent skill for cost and impact

[`skills/xf-checks`](skills/xf-checks/SKILL.md) is an [Agent Skill](https://agentskills.io) that teaches a coding agent (Claude Code, Codex and others that read skills) to run `xf cost --json` and `xf impact --json` on a branch and read what they report: what to fix, what is a bound, what wasn't checked. Install it where your agent looks for skills, for every project or one. For Claude Code, from a checkout of this repository:

```bash
mkdir -p ~/.claude/skills && cp -R skills/xf-checks ~/.claude/skills/
```

Other agents look elsewhere:

| Agent | Every project | One project |
|---|---|---|
| Claude Code | `~/.claude/skills/` | `.claude/skills/` |
| Codex | `~/.agents/skills/` | `.agents/skills/` |
| Antigravity | `~/.gemini/config/skills/` | `.agents/skills/` |
| T3 Code | where the agent it runs looks | |

A project's folder is shared with its team through git. The skill grills you on each critical change before fixing anything, with the agent's own question tool when it has one (T3 Code shows Claude Code's and Codex's), else as a numbered list in its reply. Then ask the agent what your branch costs, or whether it breaks anything downstream. Copy it again when you update xf, so it matches the flags of the `xf` you run.

## Optional: settings and API runs

xf works with no config. To run actions in GCP through the Dataform API instead of `dataform run`, commit a `.xf/config.json` at the project root. `xf init` writes one, working out the Dataform repository from your `origin` remote and looking up its region:

```bash
xf init
```

```json
{
  "runMode": "api",
  "dataformLocation": "europe-west2",
  "dataformRepo": "my_repo",
  "defaultBranch": "main"
}
```

API runs compile the branch you pushed to `origin`, not the files on disk. While `a` is on, the Run pane and the R, T, I and C overlays turn blue, their titles end `· GCP`, and a line under the toggles names what runs: `runs origin/feat-orders · 2 uncommitted files, 1 unpushed commit won't run · 3 unpulled commits will run`. In `xf web`, hovering a count lists the files or commits. The line is read again when a file changes and when the terminal or page gets focus back. A branch that isn't on `origin` (as of the last fetch) can't run: `r` then stops and asks you to push first.

Once the run has actions, the log pane (`4`) shows them as a table, as the extension's workflow run table does: each action's state, how long its BigQuery job took, the slot time it used, the bytes it was billed and what they cost at the on-demand price ($6.25 per TiB). A line over the table tells how far the run has got and its totals so far, which grow as each job finishes: `2/3 done ▶ 1 running elapsed 1m 15s slot 1m 05s billed 3.01 GiB est. $0.0184`. On a narrow pane the times go first, then the bytes, before the cost. `t` swaps the table and the log, `enter` opens the BigQuery job of the action under the cursor and `u` the workflow invocation. A wide pane (`z` zooms) adds why a failed action failed.

| Target | State | Duration | Slot time | Billed | Est. cost |
|---|---|---|---|---|---|
| analytics.daily_orders | ✓ succeeded | 3.2s | 1m 04s | 3.00 GiB | $0.0183 |
| analytics.daily_orders_check | ✗ failed | 1.5s | 0.5s | 10.00 MiB | $0.0001 |

Over the table, `/` searches the jobs: every word typed must be in the target, the state or the failure (`enter` keeps the search, `esc` clears it). `S` sorts by the next column (duration, slot time, billed, cost, target, state, then the run's order again): numbers biggest first, with jobs that have none yet last, and by state what needs looking at first. Elsewhere `/` still finds an action and `S` still shows the columns. In `xf web`, clicking a column's header also sorts by it, again turns it round, and a third time goes back to the run's order.

**Watching a workflow xf didn't start (xf web only).** A scheduled workflow, a colleague's, one started from the console: open the palette (`cmd+shift+p`), pick *Run: watch a workflow by ID or URL*, and paste its ID (`1700000000-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee`) or the URL of its page in the Cloud console. xf reads the workflow with the Dataform API and shows the jobs table above, the cost of each action's BigQuery job included, then keeps it in the history as `external`; re-pasting the same workflow updates that row. A workflow that is still running is followed until it ends, the table growing as each job finishes. `ctrl+c` stops watching it and never cancels it. An ID alone is looked for in this project's Dataform repository (`dataformRepo`, else the checkout's `origin`, in `dataformLocation`); a URL (only a `https://console.cloud.google.com` one is read) names its own project, location and repository, so it works for any workflow your login can see, whichever project it is in. xf only reads: it never runs, changes or cancels anything in GCP, and what you paste is checked character by character before it goes into any request. A job is looked for in the project's BigQuery location, then the workflow's repository's, then with none.

Personal defaults go in `~/.config/xf/config.json` (`%AppData%\xf\config.json` on Windows). Flags win over both files. To see which settings are in effect and where each came from:

```bash
xf config
```

Inside the TUI, `,` shows the settings the running xf started with, beside the project's full path, the xf and `dataform` or `dbt` binaries and their versions, the Google accounts, the git branch, the editor and where the engine logs. `y` there copies it all, for a bug report.

### Which Google account xf uses

xf's own calls to BigQuery, Data Lineage and the Dataform API sign in with Application Default Credentials: the login `gcloud auth application-default login` writes, or the file `$GOOGLE_APPLICATION_CREDENTIALS` names. That is not the gcloud CLI's account (`gcloud auth login`), and the two can differ. To see both:

```bash
xf config --auth
```

```
google cloud
  xf uses  ashish@work.example
           user login
           /Users/ashish/.config/gcloud/application_default_credentials.json  gcloud's default file
  quota    old-sandbox
  gcloud   ashish@home.example  project my-proj
           differs from the account xf uses
```

`xf uses` is the account, the kind of credentials and the file they came from. `quota` is the project xf's calls are counted against (`gcloud auth application-default set-quota-project` changes it). xf asks Google for the account each time, so a login that has expired shows here, with how to sign in again. The `,` overlay has the same section. In a dbt project it covers xf's own calls only: dbt signs in as `profiles.yml` says.

`--json` prints `xf config` as JSON, for scripts, editors and agents. These names are stable:

```bash
xf config --auth --json
```

```json
{
  "project": "/Users/ashish/repos/orders",
  "configFiles": [{"path": "/Users/ashish/.config/xf/config.json", "found": true}],
  "settings": [{"key": "runMode", "value": "cli", "from": "default"}],
  "warnings": [],
  "googleCloud": {
    "adc": {
      "account": "ashish@work.example",
      "type": "authorized_user",
      "source": "/Users/ashish/.config/gcloud/application_default_credentials.json",
      "sourceFrom": "gcloud's default file",
      "quotaProject": "old-sandbox"
    },
    "gcloud": {"account": "ashish@home.example", "project": "my-proj"}
  }
}
```

`googleCloud` is there only with `--auth`. `type` is the credential file's own: `authorized_user` (a user login), `service_account`, `impersonated_service_account`, `external_account`, or `metadata_server` for credentials that come from the machine, as in Cloud Shell. `adc` or `gcloud` has an `error` when its lookup failed, e.g. no login or no gcloud on `PATH`; the command still exits 0.

`o` opens the selected action's file in the editor you used last (one running `xf lsp`), or in the neovim xf runs beside. With neither, it uses the `editor` setting's command. Unset, that is `$VISUAL`, then `$EDITOR`, then `code` (VS Code):

```json
{
  "editor": "nvim"
}
```

| `editor` | `o` on an action | `o` on a project in the projects list |
|---|---|---|
| `code`, `cursor`, `windsurf`, `codium`, `zed`, `subl` | opens the project's folder with the file at the action's line: in the window that has the project, or a new one | opens the project's folder; the list stays |
| `nvim` | replaces xf with neovim on the file and xf in a split beside it, as `xf --nvim` does; not while a run is in progress | the same, for that project |
| `vim`, `vi`, `hx`, `nano`, `micro` | takes over xf's terminal, in the project, until you quit it | the same, on the folder; quitting returns to the list |
| anything else | gets the file, without a line | gets the folder |

The folder is always the project's root, also when that is a subfolder of a repository. In the list, `o` uses the editor of the project under the cursor, so a project's `.xf/config.json` can name its own. The command can carry arguments (`"code --reuse-window"`), and `--editor` sets it for one run. `nvim` isn't opened from inside a neovim terminal (press `enter` there) or on Windows; the footer says why.

`xf --help` documents every flag and key. If `dataform` isn't on your `PATH`, point xf at it with `--dataform-bin`, `$DATAFORM_BIN` or `"dataformBin"` in a config file; for `dbt`, with `--dbt-bin`, `$DBT_BIN` or `"dbtBin"`.

## Troubleshooting

- **`xf: not a Dataform or dbt project: … has none of workflow_settings.yaml, dataform.json, dbt_project.yml`**: run xf in the project's root (not a subdirectory such as `definitions/` or `models/`), or pass `--dir`.
- **`… is both a Dataform and a dbt project`**: pass `--backend dataform` or `--backend dbt`.
- **A dbt project shows `not compiled`**: you're on dbt v2 and the project has `on-run-start` or `on-run-end` hooks; see [dbt projects](#dbt-projects).
- **Dry runs say to log in**: run `gcloud auth application-default login` (step 2).
- **`xf impact` in CI says `neither origin/main nor main exists`, or finds no common ancestor**: the job fetched too little history; fetch the default branch (`fetch-depth: 0`).
- **Column impact says "no access to Data Lineage"**: enable the Data Lineage API in the tables' project and ask for `roles/datalineage.viewer`. With user credentials, the quota project in your ADC file (`gcloud auth application-default set-quota-project`) needs the API enabled too.
- **No `xf` client in `:checkhealth vim.lsp`**: check that `xf` is on the `PATH` neovim sees (`:!which xf`; on Windows, `Get-Command xf` in PowerShell), and that the buffer is inside a directory with `workflow_settings.yaml` (or `dbt_project.yml`).
- **"the xf on PATH is older than this plugin"**: update xf as you installed it ([Install](#1-install-xf)), then run `:lsp restart`.
- **Copying (`y`) does nothing on Linux**: install `xclip`, `xsel` or `wl-clipboard`.
- **The engine misbehaves**: it logs to `xf-<hash>-<tool>-<variant>-v<n>.log` in `$TMPDIR` (or `/tmp`), next to its socket; on Windows, in `%TEMP%` (`Get-ChildItem $env:TEMP\xf-*.log`). `xf stop` stops every engine you run, and the next `xf` starts a fresh one.
- **Windows: upgrading fails with "Access is denied"**, or `just install` can't remove an old `xf.exe.old-…`: an xf is still running. Run `xf stop` and close what it lists ([Upgrading](#upgrading)).
- **Windows: `xf` isn't recognised**: its folder isn't on your `PATH` (`$env:Path -split ';'` lists it), or the terminal was opened before you added it.
- **Windows: `xf.exe` vanished**: Windows Defender may have quarantined it ([Running a downloaded `xf.exe`](#running-a-downloaded-xfexe)).

## Development

New features go to xf web (`internal/web`) only; the TUI (`internal/ui`) takes fixes for what breaks it and nothing new. What both front ends share (the side effects they're given, and how they find, word and run things) is in `internal/frontend`. xf web must not import `internal/ui` or a terminal library, which a test in `internal/web` checks.

`just test`, `just lint` and `just build` do what they say. GitHub CI runs only on Windows, on pushes to `main`, pull requests and `v*` tags, so `just check` runs `go vet` and `go test` on your machine, and `just check-linux` runs the same in a Linux container (it needs Docker running) for changes that may behave differently there. Run `just install-hooks` once per clone and pushing `main` runs `just check` for you; `git push --no-verify` skips it. The tests read committed compile fixtures, so they need neither CLI; `just fixtures-examples` regenerates the ones in `testdata/examples` from the [example projects](examples), using `dataform`, the `dbt` on your `PATH` (dbt v2) and `$XF_DBT_CORE_BIN` (a dbt-core 1.x). `XF_E2E=1 go test ./...` also compiles the examples with the real CLIs. `just release-snapshot` builds the release archives for every OS and architecture into `dist/` with GoReleaser, as CI does, and `just vsix target=<target>` packs the [VS Code test extension](editors/vscode).

## License

xf is released under the [MIT License](LICENSE).

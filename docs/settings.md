# Settings and API runs

xf works with no config.
To run actions in GCP through the Dataform API instead of `dataform run`, commit a `.xf/config.json` at the project root.
`xf init` writes one, working out the Dataform repository from your `origin` remote and looking up its region:

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

API runs compile the branch you pushed to `origin`, not the files on disk.
While `a` is on, the Run pane and the R, T, I and C overlays turn blue, their titles end `· GCP`, and a line under the toggles names what runs: `runs origin/feat-orders · 2 uncommitted files, 1 unpushed commit won't run · 3 unpulled commits will run`.
In `xf web`, hovering a count lists the files or commits.
The line is read again when a file changes and when the terminal or page gets focus back.
A branch that isn't on `origin` (as of the last fetch) can't run: `r` then stops and asks you to push first.

Once the run has actions, the log pane (`4`) shows them as a table, as the extension's workflow run table does: each action's state, how long its BigQuery job took, the slot time it used, the bytes it was billed and what they cost at the on-demand price ($6.25 per TiB).
A line over the table tells how far the run has got and its totals so far, which grow as each job finishes: `2/3 done ▶ 1 running elapsed 1m 15s slot 1m 05s billed 3.01 GiB est. $0.0184`.
On a narrow pane the times go first, then the bytes, before the cost.
`t` swaps the table and the log, `enter` opens the BigQuery job of the action under the cursor and `u` the workflow invocation.
A wide pane (`z` zooms) adds why a failed action failed.

| Target | State | Duration | Slot time | Billed | Est. cost |
|---|---|---|---|---|---|
| analytics.daily_orders | ✓ succeeded | 3.2s | 1m 04s | 3.00 GiB | $0.0183 |
| analytics.daily_orders_check | ✗ failed | 1.5s | 0.5s | 10.00 MiB | $0.0001 |

Over the table, `/` searches the jobs: every word typed must be in the target, the state or the failure (`enter` keeps the search, `esc` clears it).
`S` sorts by the next column (duration, slot time, billed, cost, target, state, then the run's order again): numbers biggest first, with jobs that have none yet last, and by state what needs looking at first.
Elsewhere `/` still finds an action and `S` still shows the columns.
In `xf web`, clicking a column's header also sorts by it, again turns it round, and a third time goes back to the run's order.

**Watching a workflow xf didn't start (xf web and `xf watch`).**
A scheduled workflow, a colleague's, one started from the console: open the palette (`cmd+shift+p`), pick *Run: watch a workflow by ID or URL*, and paste its ID (`1700000000-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee`) or the URL of its page in the Cloud console. xf reads the workflow with the Dataform API and shows the jobs table above, the cost of each action's BigQuery job included, then keeps it in the history as `external`; re-pasting the same workflow updates that row.
A workflow that is still running is followed until it ends, the table growing as each job finishes.
`ctrl+c` stops watching it and never cancels it.
An ID alone is looked for in this project's Dataform repository (`dataformRepo`, else the checkout's `origin`, in `dataformLocation`); a URL (only a `https://console.cloud.google.com` one is read) names its own project, location and repository, so it works for any workflow your login can see, whichever project it is in. xf only reads: it never runs, changes or cancels anything in GCP, and what you paste is checked character by character before it goes into any request.
A job is looked for in the project's BigQuery location, then the workflow's repository's, then with none.

From the command line, `xf watch <ID or URL>` does the same: it follows the workflow until it ends, the log on stderr, then prints the jobs table (or `--json`, the figures as numbers) on stdout.
In a Dataform project it keeps the workflow in the history too; outside one, a URL is enough.
It exits 0 when the workflow succeeded, 1 when it failed or was cancelled, 4 when xf couldn't read it, and 130 when `ctrl+c` stopped watching.
`xf watch --help` lists the flags (`--dir`, `--dataform-repo`, `--dataform-location`, `--json`).

Personal defaults go in `~/.config/xf/config.json` (`%AppData%\xf\config.json` on Windows).
Flags win over both files.
To see which settings are in effect and where each came from:

```bash
xf config
```

Inside the TUI, `,` shows the settings the running xf started with, beside the project's full path, the xf and `dataform` or `dbt` binaries and their versions, the Google accounts, the git branch, the editor and where the engine logs.
`y` there copies it all, for a bug report.

## Which Google account xf uses

xf's own calls to BigQuery, Data Lineage and the Dataform API sign in with Application Default Credentials: the login `gcloud auth application-default login` writes, or the file `$GOOGLE_APPLICATION_CREDENTIALS` names.
That is not the gcloud CLI's account (`gcloud auth login`), and the two can differ.
To see both:

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

`xf uses` is the account, the kind of credentials and the file they came from.
`quota` is the project xf's calls are counted against (`gcloud auth application-default set-quota-project` changes it). xf asks Google for the account each time, so a login that has expired shows here, with how to sign in again.
The `,` overlay has the same section.
In a dbt project it covers xf's own calls only: dbt signs in as `profiles.yml` says.

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

`googleCloud` is there only with `--auth`.
`type` is the credential file's own: `authorized_user` (a user login), `service_account`, `impersonated_service_account`, `external_account`, or `metadata_server` for credentials that come from the machine, as in Cloud Shell.
`adc` or `gcloud` has an `error` when its lookup failed, e.g. no login or no gcloud on `PATH`; the command still exits 0.

`o` opens the selected action's file in the editor you used last (one running `xf lsp`), or in the neovim xf runs beside.
With neither, it uses the `editor` setting's command.
Unset, that is `$VISUAL`, then `$EDITOR`, then `code` (VS Code):

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

The folder is always the project's root, also when that is a subfolder of a repository.
In the list, `o` uses the editor of the project under the cursor, so a project's `.xf/config.json` can name its own.
The command can carry arguments (`"code --reuse-window"`), and `--editor` sets it for one run.
`nvim` isn't opened from inside a neovim terminal (press `enter` there) or on Windows; the footer says why.

`xf --help` documents every flag and key.
If `dataform` isn't on your `PATH`, point xf at it with `--dataform-bin`, `$DATAFORM_BIN` or `"dataformBin"` in a config file; for `dbt`, with `--dbt-bin`, `$DBT_BIN` or `"dbtBin"`.

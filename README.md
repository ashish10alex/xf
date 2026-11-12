# xf

Cost and breakage checks for [Dataform](https://github.com/dataform-co/dataform) and [dbt](https://www.getdbt.com/) branches on BigQuery, before they merge.

- **`xf cost`**: what the branch's changed actions would scan and cost, and the change from the same actions at its merge-base with `main`.
- **`xf impact`**: the columns and tables it drops or retypes that something downstream still reads, from Dataplex lineage.
- **`xf check`**: both, as one comment on the pull request and notes on its changed lines, with gates that can fail the job.

They only dry run and read metadata: free in BigQuery, and nothing is run.
Run them in [GitHub Actions or GitLab CI](#in-ci), give them to [a coding agent](#for-coding-agents) as JSON, or open the same checks in [xf web](#xf-web), the browser UI.

```text
$ xf cost --fail-over 1TB
4 changed actions in feat/orders-v2 vs origin/main @ 4e1c9a2 (working tree)
dry run in analytics-prod (US) · compiled with prodCompilerOptions
⚠ marts.fct_order_items      1.42 TiB   $8.89  new                   [MISSING PARTITION FILTER: order_date on staging.order_lines]
✓ marts.fct_orders         214.08 GiB   $1.31  -96.12 GiB (-$0.587)  (was 310.20 GiB · $1.89)
✓ marts.agg_daily_revenue   38.50 GiB  $0.235  +12.75 GiB (+$0.078)  (was 25.75 GiB · $0.157)
✓ marts.dim_customers        4.67 GiB  $0.028  no change
────────────────────────────────────────────────────────────
Total scanned: 1.67 TiB | Estimated cost: $10.46
✗ Gate failed: 1.67 TiB exceeds the limit of 1.00 TiB
```

xf works out from the files in a project's root whether it is Dataform or dbt. dbt projects get most of what Dataform's do; [dbt projects](docs/dbt.md) lists the differences.
Site: [xf.dataformtools.com](https://xf.dataformtools.com).

## What you need

- For Dataform, the `dataform` CLI (`npm i -g @dataform/cli`).
  For dbt, dbt-core 1.x with the BigQuery adapter (`pip install dbt-core dbt-bigquery`) or dbt v2 (`brew install dbt-labs/dbt/dbt`).
- `gcloud`, logged in for Application Default Credentials ([below](#log-in-to-gcp)).
- macOS, Linux or Windows 10 22H2 / 11 (x64 or ARM64; see [Windows](docs/windows.md)).

## Install

xf is a single executable. On Windows, see [Windows](docs/windows.md#xf-on-windows).

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

To update later, run `xf update` (or `xf --update`).
It updates xf the way it was installed: `brew upgrade --cask` for Homebrew, `go install …@latest` for Go, and otherwise replaces `xf` with the latest release after checking it against the release's checksums.
A build from source says to pull and rebuild instead.
The neovim plugin warns you if the `xf` on your `PATH` is older than the plugin.

To build from source, clone the repo and run `go install ./cmd/xf` (or `just install`, with [`just`](https://github.com/casey/just)).

## Log in to GCP

Dry runs, table hovers and API runs call GCP as you, through Application Default Credentials, which are separate from your `gcloud` login:

```bash
gcloud auth application-default login
```

xf picks up a new login without a restart.
`dataform run` uses the Dataform CLI's own credentials, so a Dataform project also needs a `.df-credentials.json` (`dataform init-creds`).

## Check a branch

From a project's root (the folder with `workflow_settings.yaml`, `dataform.json` or `dbt_project.yml`), on the branch to check:

```bash
git fetch origin main   # xf compares with the merge-base, and never fetches
xf cost                 # what the changed actions scan, and cost
xf impact               # who still reads the columns they drop or retype
xf check                # both, as the Markdown a pull request gets
```

Each prints a report and exits 0; `2` means the check couldn't be made, with the reason on stderr.
`--fail-over 100GB` (cost) and `--fail-on critical` (impact) turn them into gates that exit 1, and `--json` prints JSON for scripts and agents.

Compare with prod's tables, not your dev ones: set `prodCompilerOptions` in the project's `.xf/config.json` (`dbtProdTarget` for dbt), and commit it so CI uses it too.
Until it is set, every report says it compared with the default targets.
[Checking a branch](docs/checks.md) has every flag, the report formats and the roles the checks need.

## In CI

`xf check` posts one comment on the pull request (a note on a GitLab merge request), edited on each push, and notes on the lines that drop a column or read a table.
It reports and passes by default; `fail-on` and `fail-over` make it a gate.
`xf check init github` or `xf check init gitlab` writes the job for your repository, with the commands to set up workload identity federation.

GitHub Actions, in `.github/workflows/xf-check.yml` (the xf repository is the action; `<tag>` is an xf release, such as `v0.0.5`):

```yaml
name: xf check
on:
  pull_request:

jobs:
  check:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write        # for workload identity federation
      pull-requests: write   # to post the comment
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0     # xf needs history to find the merge-base
      - uses: google-github-actions/auth@v2
        with:
          workload_identity_provider: projects/123456789/locations/global/workloadIdentityPools/github/providers/github
          service_account: xf-check@my-project.iam.gserviceaccount.com
      - uses: ashish10alex/xf@<tag>
        with:
          fail-on: critical  # a dropped column something still reads
          fail-over: 1TB     # the changed actions scan more than 1 TiB
```

GitLab CI, in `.gitlab-ci.yml`, with two CI/CD variables: `XF_FORGE_TOKEN` (a project access token with the `api` scope) and `GOOGLE_APPLICATION_CREDENTIALS` (a File variable):

```yaml
include:
  - remote: https://raw.githubusercontent.com/ashish10alex/xf/<tag>/ci/gitlab/xf-check.yml

xf-check:
  variables:
    XF_VERSION: <tag>
    XF_FAIL_ON: critical
    XF_FAIL_OVER: 1TB
```

Elsewhere (Cloud Build, Jenkins), download the release and run `xf check --output check.md`; [Other CI systems](docs/checks.md#other-ci-systems) has the steps.

## For coding agents

[`skills/xf-checks`](skills/xf-checks/SKILL.md) is an [Agent Skill](https://agentskills.io) that has Claude Code, Codex and other agents that read skills run `xf cost --json` and `xf impact --json` on a branch, read the `severity` of each change, and ask you about each critical one before fixing anything.
[`skills/xf-run`](skills/xf-run/SKILL.md) starts every run as a free `xf run --dry-run --json` plan, and runs it only once you say yes.
For Claude Code, from a checkout of this repository:

```bash
mkdir -p ~/.claude/skills && cp -R skills/xf-checks skills/xf-run ~/.claude/skills/
```

Other agents look elsewhere; see [Agent skill for cost and impact](docs/checks.md#agent-skill-for-cost-and-impact).

## xf web

To look around instead, `xf web` opens the project in your browser: each action's compiled SQL, columns, lineage and dry-run cost, the source to edit, and the impact and cost reports on its Checks page.

```bash
xf web
```

Press `?` for every key: `⌘p` (`ctrl+p`) finds an action, `r` runs it, `R` runs what your branch changed, `I` and `C` check its columns and cost, `E` edits its file.
Outside a project, xf web lists the projects you have opened.
To try it without a project of your own, use an example: `xf web --dir examples/dataform` or `xf web --dir examples/dbt`.

Prefer the terminal?
`xf` opens the same panes as a TUI, and `xf --nvim` opens neovim with xf in a split beside it.
The TUI is in maintenance mode: it keeps working, but new features go to xf web.

## Documentation

- [Checking a branch](docs/checks.md): query cost (`xf cost`) and column impact (`xf impact`), in xf and in CI; `xf check`, its GitHub Action and its GitLab template, which put both on a pull request, and `xf check init`, which writes the CI job; and the agent skill for both.
- [Running and branches](docs/running.md): Run Changed, tag runs, switching branch, deferring to prod, and `xf run` with `--defer` from the command line, with its agent skill.
- [xf web](docs/xf-web.md): the browser UI, with the source editor, file tree, source control, search, Scratch queries, the dependency inspector and settings.
- [dbt projects](docs/dbt.md): what works in a dbt project and how it differs.
- [Settings and API runs](docs/settings.md): the config files, running through the Dataform API, and which Google account xf uses.
- [The TUI](docs/tui.md): the terminal UI, its keys, and the project list.
- [neovim](docs/neovim.md): `xf --nvim`, the plugin and the language server.
- [Windows](docs/windows.md): installing, upgrading and what differs on Windows.
- [Troubleshooting](docs/troubleshooting.md).

## Development

New features go to xf web (`internal/web`) only; the TUI (`internal/ui`) takes fixes for what breaks it and nothing new.
What both front ends share (the side effects they're given, and how they find, word and run things) is in `internal/frontend`. xf web must not import `internal/ui` or a terminal library, which a test in `internal/web` checks.

`just test`, `just lint` and `just build` do what they say.
GitHub CI runs only on Windows, on pushes to `main`, pull requests and `v*` tags, so `just check` runs `go vet` and `go test` on your machine, and `just check-linux` runs the same in a Linux container (it needs Docker running) for changes that may behave differently there.
Run `just install-hooks` once per clone and pushing `main` runs `just check` for you; `git push --no-verify` skips it.
The tests read committed compile fixtures, so they need neither CLI; `just fixtures-examples` regenerates the ones in `testdata/examples` from the [example projects](examples), using `dataform`, the `dbt` on your `PATH` (dbt v2) and `$XF_DBT_CORE_BIN` (a dbt-core 1.x).
`XF_E2E=1 go test ./...` also compiles the examples with the real CLIs.
`just release-snapshot` builds the release archives for every OS and architecture into `dist/` with GoReleaser, as CI does, and `just vsix target=<target>` packs the [VS Code test extension](editors/vscode).

## License

xf is released under the [MIT License](LICENSE).

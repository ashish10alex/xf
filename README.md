# xf

A unified interface for [Dataform](https://github.com/dataform-co/dataform) and [dbt](https://www.getdbt.com/) projects on BigQuery. Browse a project's actions with their compiled SQL, columns and lineage, dry run and run them, edit the source with vim keys, and see what your branch changes: the actions it touches, the columns it drops or retypes that something downstream still reads, and what its queries would cost.

xf works out from the files in a project's root whether it is Dataform or dbt. dbt projects get most of what Dataform's do; [dbt projects](docs/dbt.md) lists the differences.

## What you need

- For Dataform, the `dataform` CLI (`npm i -g @dataform/cli`). For dbt, dbt-core 1.x with the BigQuery adapter (`pip install dbt-core dbt-bigquery`) or dbt v2 (`brew install dbt-labs/dbt/dbt`).
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

To update later, run `xf update` (or `xf --update`). It updates xf the way it was installed: `brew upgrade --cask` for Homebrew, `go install …@latest` for Go, and otherwise replaces `xf` with the latest release after checking it against the release's checksums. A build from source says to pull and rebuild instead. The neovim plugin warns you if the `xf` on your `PATH` is older than the plugin.

To build from source, clone the repo and run `go install ./cmd/xf` (or `just install`, with [`just`](https://github.com/casey/just)).

## Log in to GCP

Dry runs, table hovers and API runs call GCP as you, through Application Default Credentials, which are separate from your `gcloud` login:

```bash
gcloud auth application-default login
```

xf picks up a new login without a restart. `dataform run` uses the Dataform CLI's own credentials, so a Dataform project also needs a `.df-credentials.json` (`dataform init-creds`).

## Quick start

From a project's root (the folder with `workflow_settings.yaml`, `dataform.json` or `dbt_project.yml`), open xf web:

```bash
xf web
```

It opens in your browser. Press `?` for every key: `⌘p` (`ctrl+p`) finds an action, `r` runs it, `R` runs what your branch changed, `I` and `C` check its columns and cost, `E` edits its file. Outside a project, xf web lists the projects you have opened. To try it without a project of your own, use an example: `xf web --dir examples/dataform` or `xf web --dir examples/dbt`.

Prefer the terminal? `xf` opens the same panes as a TUI, and `xf --nvim` opens neovim with xf in a split beside it. The TUI is in maintenance mode: it keeps working, but new features go to xf web.

## Documentation

- [xf web](docs/xf-web.md): the browser UI, with the source editor, file tree, source control, search, Scratch queries, the dependency inspector and settings.
- [The TUI](docs/tui.md): the terminal UI, its keys, and the project list.
- [neovim](docs/neovim.md): `xf --nvim`, the plugin and the language server.
- [Running and branches](docs/running.md): Run Changed, tag runs, switching branch, deferring to prod, and `xf run` with `--defer` from the command line, with its agent skill.
- [Checking a branch](docs/checks.md): column impact (`xf impact`) and query cost (`xf cost`), in xf and in CI, and the agent skill for both.
- [dbt projects](docs/dbt.md): what works in a dbt project and how it differs.
- [Settings and API runs](docs/settings.md): the config files, running through the Dataform API, and which Google account xf uses.
- [Windows](docs/windows.md): installing, upgrading and what differs on Windows.
- [Troubleshooting](docs/troubleshooting.md).

## Development

New features go to xf web (`internal/web`) only; the TUI (`internal/ui`) takes fixes for what breaks it and nothing new. What both front ends share (the side effects they're given, and how they find, word and run things) is in `internal/frontend`. xf web must not import `internal/ui` or a terminal library, which a test in `internal/web` checks.

`just test`, `just lint` and `just build` do what they say. GitHub CI runs only on Windows, on pushes to `main`, pull requests and `v*` tags, so `just check` runs `go vet` and `go test` on your machine, and `just check-linux` runs the same in a Linux container (it needs Docker running) for changes that may behave differently there. Run `just install-hooks` once per clone and pushing `main` runs `just check` for you; `git push --no-verify` skips it. The tests read committed compile fixtures, so they need neither CLI; `just fixtures-examples` regenerates the ones in `testdata/examples` from the [example projects](examples), using `dataform`, the `dbt` on your `PATH` (dbt v2) and `$XF_DBT_CORE_BIN` (a dbt-core 1.x). `XF_E2E=1 go test ./...` also compiles the examples with the real CLIs. `just release-snapshot` builds the release archives for every OS and architecture into `dist/` with GoReleaser, as CI does, and `just vsix target=<target>` packs the [VS Code test extension](editors/vscode).

## License

xf is released under the [MIT License](LICENSE).

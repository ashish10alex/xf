## Working on xf

- **New features** go to xf web alone, unless the user names the TUI; the TUI (`internal/ui`) is in maintenance mode and takes only fixes for what breaks it. Logic both front ends need lives in `internal/frontend`, never imported by xf web from `internal/ui`.

- **Layout**: `cmd/xf/` CLI entry; `internal/web/` Go server (`<feature>.go`), native ES module UI (`assets/`), and `node:test` suite (`jstest/`); `internal/frontend/` shared logic; `internal/backend/` Dataform & dbt; `internal/warehouse/` + `bigquery`/`databricks`/`snowflake` warehouse clients.

- **Commit subjects** are [Conventional Commits](https://www.conventionalcommits.org), as the release notes group by type: `feat(web): inline git blame on the cursor's line`.
  The scope is the area, one lowercase word: `web`, `tui`, `engine`, `lsp`, `nvim`, a subcommand (`check`, `run`), `release`, `agents`, `adr`.
  `feat`, `fix` and `perf` reach the notes; `docs`, `test`, `ci`, `build`, `chore` and `refactor` don't. `!` before the colon marks a breaking change.
  `.githooks/commit-msg` and `just ship` refuse any other subject.

- **Checking a change** (Go/race tests, `just jstest`, Windows path rules, TUI in tmux, xf web browser preview, BigQuery limits): `docs/agents/checking-changes.md`.
- **xf web** (`internal/web` and its page): `docs/agents/xf-web.md`.
- **Parallel sessions** (worktrees, shipping with `just ship`, worktree preview, committing beside another session's edits): `docs/agents/parallel-sessions.md`.
- **Releasing** (`just release-preflight`, tagging a release, a failed release): `docs/agents/releasing.md`.

## Agent skills

### Issue tracker

Issues and specs live in GitHub Issues on `ashish10alex/xf`, worked with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: one `GLOSSARY.md` at the repo root (created lazily) and ADRs in `docs/adr/`. See `docs/agents/domain.md`.

## Working on xf

- **New features** go to xf web alone, unless the user names the TUI; the TUI (`internal/ui`) is in maintenance mode and takes only fixes for what breaks it. Logic both front ends need lives in `internal/frontend`, never imported by xf web from `internal/ui`.

- **Commit subjects** are [Conventional Commits](https://www.conventionalcommits.org), as the release notes group by type: `feat(web): inline git blame on the cursor's line`.
  The scope is the area, one lowercase word: `web`, `tui`, `engine`, `lsp`, `nvim`, a subcommand (`check`, `run`), `release`, `agents`, `adr`.
  `feat`, `fix` and `perf` reach the notes; `docs`, `test`, `ci`, `build`, `chore` and `refactor` don't. `!` before the colon marks a breaking change.
  `.githooks/commit-msg` and `just ship` refuse any other subject.

- **Checking a change** (Go tests, the TUI in tmux, xf web in the browser preview, BigQuery limits): `docs/agents/checking-changes.md`.
- **xf web** (`internal/web` and its page): `docs/agents/xf-web.md`.
- **Parallel sessions** (a worktree per session, shipping with `just ship`, the worktree preview, committing beside another session's edits): `docs/agents/parallel-sessions.md`.
- **Releasing** (tagging a release, a failed release): `docs/agents/releasing.md`.

## Agent skills

### Issue tracker

Issues and specs live in GitHub Issues on `ashish10alex/xf`, worked with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: one `GLOSSARY.md` at the repo root (created lazily) and ADRs in `docs/adr/`. See `docs/agents/domain.md`.

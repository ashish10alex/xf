## Working on xf

- **New features** go to xf web alone, unless the user names the TUI; the TUI (`internal/ui`) is in maintenance mode and takes only fixes for what breaks it. Logic both front ends need lives in `internal/frontend`, never imported by xf web from `internal/ui`.

- **Checking a change** (Go tests, the TUI in tmux, xf web in the browser preview, BigQuery limits): `docs/agents/checking-changes.md`.
- **xf web** (`internal/web` and its page): `docs/agents/xf-web.md`.
- **Parallel sessions** (a worktree per session, shipping with `just ship`, the worktree preview, committing beside another session's edits): `docs/agents/parallel-sessions.md`.
- **Releasing** (tagging a release, a failed release): `docs/agents/releasing.md`.

## Agent skills

### Issue tracker

Issues and specs live in GitHub Issues on `ashish10alex/xf`, worked with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: one `GLOSSARY.md` at the repo root (created lazily) and ADRs in `docs/adr/`. See `docs/agents/domain.md`.

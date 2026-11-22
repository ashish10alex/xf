# Parallel sessions

Several sessions often work on xf at once.
Use an isolated git worktree per feature session (in T3 Code, start a worktree thread or call `t3_worktree_handoff`), so tests run on your own work and commits hold only your hunks.

## In a worktree

Everything in `docs/agents/checking-changes.md` works there, with these differences:

- **Ship** (when the user says so): commit in the worktree, then run `just ship` right from the worktree (it auto-detects the branch, pushes to `origin/main`, fast-forwards the main checkout, installs `xf`, and leaves your live worktree directory intact).
  Running `just ship <branch>` from the main checkout instead also removes the worktree and deletes `<branch>`.
- **Skip `just install` in a worktree**: it would put an unshipped branch's `xf` in `~/go/bin` and `~/.local/bin`, which the user and nvim run. `just ship` installs the pushed commit.
- **Preview**: run `scripts/preview-bg.sh [dataform|dbt]` from the worktree to start this checkout's server on a free port, open the printed URL in the browser preview, and stop it with `scripts/preview-bg.sh stop <port>`.

## Shipping

`scripts/ship.sh` (`just ship [branch]`) is the whole ship after committing, in one call.
It rebases the branch on `origin/main`, checks Conventional Commit subjects, runs `just check` on that exact commit in a clean detached copy, pushes it to `main` (skipping the pre-push hook, which would check the wrong tree), fast-forwards the main checkout, installs `xf` from the pushed commit (`~/go/bin` and `~/.local/bin`, restarting `dev.xf.web`), and removes the worktree and branch when invoked from the main checkout.
Without a branch in the main checkout, it ships the main checkout's own new commits on `main`.
On a failed check or a rebase conflict it pushes nothing and says why; when the main checkout can't fast-forward because its uncommitted edits overlap, the push still stands and it says so.

## In a shared checkout

When a session works in the main checkout beside another session's uncommitted edits, commit only its own hunks and leave theirs in the working tree:

1. Before editing a file the other session has touched, snapshot it, so their hunks can be told from yours.
2. Files only you touched: `git add` them.
3. Shared files: build HEAD plus only your hunks.
   Walk HEAD's lines and apply your hunks at their exact old lines (`patch -F3` on `git show HEAD:<file>` works; place rejects by hand), then stage the result with `git update-index --cacheinfo`.
   `git apply --cached --unidiff-zero` misplaces zero-context insertions once earlier hunks are skipped, so stay away from it.
4. Commit, then `just ship`: it checks the commit alone, in a clean copy, before pushing.

# Parallel sessions

Several sessions often work on xf at once.
Each feature session gets its own git worktree, so its tests run on its own work and its commits hold only its own hunks.

## In a worktree

EnterWorktree makes the worktree under `.claude/worktrees/<name>` on a `worktree-<name>` branch.
Everything in `docs/agents/checking-changes.md` works there, with these differences:

- **Ship** (when the user says so): commit in the worktree, call ExitWorktree with `keep`, then run `just ship worktree-<name>` from the main checkout (Bash timeout 300000).
  See "Shipping" below.
- **Don't `just install`** in a worktree: it would put that branch's xf in `~/go/bin`, which the user and nvim run. `just ship` installs the pushed commit.
- **Preview**: `preview_start` runs the main checkout's `.claude/launch.json` in the main checkout, so it serves main's assets, not yours.
  Run `scripts/preview-bg.sh [dataform|dbt]` from the worktree instead: it starts this checkout's preview on a free port, waits until it answers, and prints the URL to open with `preview_start {url}`. `scripts/preview-bg.sh stop <port>` stops it.
- **Keep shell commands plain.** The worktree guard refuses a command it can't prove stays in the worktree: `git -C <elsewhere>`, compound commands mixing git with `cd`, `awk -v x=$f`, and a heredoc whose text mentions git.
  Split them, or edit with Edit.

## Shipping

`scripts/ship.sh` (`just ship [branch]`) is the whole ship after the commit, in one call from the main checkout.
It rebases the branch on `origin/main`, runs `just check` on that exact commit in a clean detached copy, pushes it to `main` (skipping the pre-push hook, which would run the same check on the wrong tree), fast-forwards the main checkout, installs xf from the pushed commit (`~/go/bin` and `~/.local/bin`, restarting `dev.xf.web`), and removes the worktree and its branch.
Without a branch it ships the main checkout's own new commits on `main`.
On a failed check or a rebase conflict it pushes nothing and says why; when the main checkout can't fast-forward because its uncommitted edits overlap, the push still stands and it says so.

## In a shared checkout

When a session works in the main checkout beside another session's uncommitted edits, commit only its own hunks and leave theirs in the working tree:

1. Before editing a file the other session has touched, snapshot it, so their hunks can be told from yours.
2. Files only you touched: `git add` them.
3. Shared files: build HEAD plus only your hunks.
   Walk HEAD's lines and apply your hunks at their exact old lines (`patch -F3` on `git show HEAD:<file>` works; place rejects by hand), then stage the result with `git update-index --cacheinfo`.
   `git apply --cached --unidiff-zero` misplaces zero-context insertions once earlier hunks are skipped, so stay away from it.
4. Commit, then `just ship`: it checks the commit alone, in a clean copy, before pushing.

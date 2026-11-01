# Parallel sessions

Several sessions often work on xf at once. Each feature session gets its own git worktree, so its tests run on its own work and its commits hold only its own hunks.

## In a worktree

The app makes the worktree under `.claude/worktrees/<name>` on a `claude/<name>` branch. Everything in `docs/agents/checking-changes.md` works there, with these differences:

- **Merge**: when the change is done and checked, rebase the branch on `main` and fast-forward `main` to it (`git rebase main`, then in the main checkout `git merge --ff-only claude/<name>`). Run `just check` on the rebased branch first.
- **Install** from the main checkout after the merge: `just install` in a worktree would put that branch's xf in `~/go/bin`, which the user and nvim run.
- **Preview**: `xf-dataform` and `xf-dbt` have fixed ports, so a preview another session started can hold one. The first line of `preview_logs` names the checkout it serves; when it names another checkout (the app reads the main checkout's `.claude/launch.json` and runs it there, so a config added in the worktree isn't seen), run the worktree's `scripts/preview.sh dataform <free port>` in the background and open its URL with `preview_start {url}`. Add `XF_PREVIEW_FRESH=1` when it says the copy is not a project.

## In a shared checkout

When a session works in the main checkout beside another session's uncommitted edits, commit only its own hunks and leave theirs in the working tree:

1. Before editing a file the other session has touched, snapshot it, so their hunks can be told from yours.
2. Files only you touched: `git add` them.
3. Shared files: build HEAD plus only your hunks. Walk HEAD's lines and apply your hunks at their exact old lines (`patch -F3` on `git show HEAD:<file>` works; place rejects by hand), then stage the result with `git update-index --cacheinfo`. `git apply --cached --unidiff-zero` misplaces zero-context insertions once earlier hunks are skipped, so stay away from it.
4. Check the staged tree alone: `git checkout-index -a --prefix=<scratch>/` and run `go build` and `go test` there.

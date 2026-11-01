---
status: accepted
---

# xf web opens branches in worktrees

xf web can check out a branch in a git worktree and open it beside the branch you are on. Each worktree's project is a project of its own (ADR 0008): it has its own engine, caches, run log and `?project=` key, and you move between them with the same snapshot and reload as any other project. It covers two jobs:

- Opening a second branch, such as a merge request to review or a hotfix, while your own branch keeps its uncommitted work. `B` refuses a switch on a dirty tree, and a worktree needs no switch.
- Opening worktrees that already exist, such as the ones agent sessions make in `.claude/worktrees`.

## Decision

**Where.** xf makes worktrees in `<git root>/.xf/worktrees/<folder>`. That folder has its own `.gitignore` of `*`, as `.xf/tmp` does. It sits at the repository's root, not the project's, so a project in a subfolder (`examples/dbt`) is not nested twice. It is kept apart from scratch's `.xf/tmp`, which can move (`scratchDir`) and whose name invites a clean-up that would delete unpushed work. A worktree made from inside another worktree still goes under the main checkout's `.xf/worktrees`. The folder is the branch name with `/` and other characters a folder can't hold turned into `-`, and `-2`, `-3` and so on on a clash.

**Making one.** In `B`, shift+enter on a branch opens it in a worktree; enter still switches. For a branch only origin has, xf makes the local branch that tracks it (`git worktree add --track -b <name> <folder> origin/<name>`), as `git switch` does. When the branch is already checked out somewhere, in the main checkout or another worktree, git would refuse it, so xf opens that checkout's project instead. `B` shows where such branches are checked out. The project in the worktree is the one at the same path from the repository's root. Making a worktree doesn't touch this checkout, so it is allowed with unsaved files or a run going. The project opens where `webOpenProjectIn` says, as any project switch does.

**From the projects tab** (amended 2026-10-27). Right-clicking a row in the column's projects tab shows a menu. Open and Open in a New Tab open the project. New Worktree… opens `B`'s list for that project's repository, for this tab's project or another open one; there, enter opens a branch in a worktree as shift+enter does in `B`. A name that no branch has gets a last row, as in `B`. That row makes the branch from that checkout's HEAD, in a new worktree (`git worktree add -b`). Remove Worktree is on worktree rows. It asks in a dialog of its own, as closing a project does, rather than opening the switcher (amended 2026-10-28). `y` or enter removes the worktree, and any other key keeps it. Its buttons do the same. The dialog stays open while the removal works and closes only once the worktree is gone. A refusal shows in the dialog, with **try again**. Copy Path is on every row.

**Naming.** A worktree's project key and name are the main checkout's project key, `@`, and the worktree's folder: `dbt@feat-x`. A worktree xf didn't make uses its own folder (`dbt@editor-stuck-logs-9ebba6`). xf tells a worktree from its `.git` file, which names `<main>/.git/worktrees/<name>`, so naming needs no git call. Worktree projects never make the main project's key fall back to a path.

**Listing.** The projects switcher and the column's projects tab list each worktree's project under its repository's main project, indented and named by branch. The open ones show their live badges. Both also list the worktrees that `git worktree list` finds and that aren't open, dimmed, so a `.claude/worktrees` checkout is a click away: the switcher those of every remembered project, and the projects tab those of every open one (amended 2026-10-27: the first build left them out of the projects tab).

**Files a worktree lacks.** A new worktree has the tracked files only. When any worktree's project opens, xf links in these from the main checkout's project, if the main checkout has them untracked and the worktree has none:

- Dataform: `node_modules`, `.df-credentials.json`
- dbt: `dbt_packages`, `profiles.yml`, `.user.yml`
- both: the project's `.xf/config.json`, so settings are shared

`node_modules` is not a link but a folder of its own, made of hard links to the main checkout's files, which take no room (amended 2026-10-28). Dataform loads a module only when its real path is in the project, so through a linked `node_modules` its compile couldn't find `@dataform/core`. A file that can't be a hard link, on another volume, is copied. A `node_modules` that an older xf linked becomes such a folder when the project next opens. It never replaces a file that is there. Scratch stays per worktree, so two open projects never edit one scratch. On Windows, where a file symlink may be refused without Developer Mode, folders are directory junctions and files are copied. A settings change made in a worktree's project then stays there.

**Dev tables.** Nothing changes. Worktrees build into the same dev tables as the main checkout, as two checkouts made by hand do.

**Removing one.** Closing a worktree's project (`q`, `x`) only closes it. Removing is its own step: the palette's `Worktree: remove this worktree` on a worktree's project, shift+del on its row in the switcher, or the projects tab's Remove Worktree. All of them ask first: shift+del in the switcher's footer, and the other two in a dialog. Any linked worktree can be removed, xf's in `.xf/worktrees` or another's, such as an agent session's in `.claude/worktrees`. For one xf didn't make, the question names its folder. A main checkout can never be removed (amended 2026-10-28: at first xf removed only the worktrees it made, and the user asked for the others too). It refuses while the project runs or a tab has unsaved changes to its files. It takes away the links it made, then looks for uncommitted or untracked work. When there is some, xf puts the links back, names the files, and asks again whether to remove the worktree anyway, losing them. Only a yes to that runs `git worktree remove --force` (amended 2026-10-28: at first xf refused and said to commit or discard the files first; the user wanted to confirm instead). A locked worktree is still refused, forced or not, with git's message. Otherwise xf closes the project, removes the worktree and forgets the project. The branch is kept.

## Why

`B` is a plain `git switch`, which refuses on a dirty tree, and it changes the one checkout every tab shows. Reviewing a merge request meant committing or stashing first, or making a worktree in a terminal and then opening it as a new project by its path. The new project then had no `node_modules` or credentials, so it failed to compile.

## Considered options

- **Worktrees in `.xf/tmp`.** It already ignores itself. But `tmp` invites deleting it, `scratchDir` can move scratch away so that the folder isn't there, and a project in a subfolder nests twice (`examples/dbt/.xf/tmp/x/examples/dbt`).
- **Outside the repository** (`~/.local/state/xf/worktrees/…`). Nothing is added to the checkout, but the worktrees are hard to find from a shell or an editor.
- **One project whose root swaps.** One entry in the lists, but every switch restarts the engine and loses the warm state that ADR 0008 keeps.
- **Copy credentials and run the install** (`npm install`, `dbt deps`). It is right per branch, but slow and needs the network. Links are instant and are wrong only when a branch changes package versions.
- **A schema suffix per worktree.** Runs from two branches would not overwrite each other, but there would be more datasets in BigQuery, and dev reads would differ per worktree. The user picked the predictable choice.
- **Removing on close.** Fewer stale checkouts, but closing would then delete things, which is surprising.
- **Never forcing a removal.** Nothing is lost by mistake, but a worktree whose changes are no longer wanted, such as a finished agent session's, had to be cleaned up in a terminal. The second question, which names the files, keeps the loss deliberate.

## Consequences

- `internal/worktree` finds, makes, links and removes worktrees. `gitbranch.List` reads each branch's `%(worktreepath)` in its one `for-each-ref`.
- Each open worktree's project holds an engine, as any open project does.
- Linked `node_modules` and `dbt_packages` are the main checkout's. A branch that changes package versions needs its own install, which replaces the link. Hard links share each file's contents, so a package edited in place, rather than installed again, changes in both.

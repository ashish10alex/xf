---
status: accepted
---

# xf web has a Source Control view beside its file tree

xf web gets a Source Control view, as VS Code's is, in the column on the far left that holds the file tree (ADR 0006). The column shows one of the two at a time: ctrl+g (ctrl on a Mac too, where cmd+g is the browser's find next) shows Source Control and gives it the keys, and pressed again while it shows hides the column; cmd+b (ctrl+b off a Mac) shows the column on the files, and hides it whichever view it shows. ctrl+g swaps the column to Source Control when the files show; cmd+b doesn't swap to the files, as in VS Code, where it closes the side bar whatever is in it. The column keeps the view it showed last, per project, with its width. ctrl+0 gives the column the keys, whichever view it shows. The column's header shows how many files have changed.

The view lists the repository's changes in sections, VS Code's names: **Merge Changes** (only while files are in conflict), **Staged Changes** (only while something is staged), **Changes** (git's unstaged and untracked files; always listed, saying "nothing to commit, working tree clean" when it is), **Stashes** (only while there is one) and **Recent commits**, with the commit message box at the top. It lists the whole repository, as a commit commits the whole index: a project in a folder of a bigger repository sees the repository's other changes too, by path from the project, and a file outside the project shows its difference but can't be edited there.

The keys are lazygit's files panel's: `j`/`k` move, enter shows the difference, space stages or unstages (marks a conflict resolved), `a` stages all, `x` discards, `c` goes to the message box (cmd+enter or ctrl+enter commits from it), `A` amends, `P` pushes, `p` pulls, `f` fetches, `s` stashes, `n` makes a branch, `/` filters. In the Stashes section space applies, `p` pops and `d` drops; in Recent commits enter shows a commit's files, `y` copies its SHA and `n` makes a branch at it.

## The difference

Enter on a changed file shows its difference in the source pane, inline unless the `diffStyle` setting says `side-by-side` (user or project config, applied at once). Side by side, it is beside what it is compared with: the index on the left, read-only, and the file itself on the right, the source pane's editor as it always is (vim, `:w`, autosave, the hunk signs). A staged file shows HEAD beside the index, and a commit's or a stash's file the commit before beside it, both sides read-only. It opens on the first change. The two sides are lined up line for line and scroll together, the lines taken out red on the left and the lines put in green on the right. `t` (outside the editor) flips between the two until the setting changes. Inline is the right side alone with the lines taken out shown above the lines that replaced them, as the `]g` peek shows one change; a narrow pane or a phone shows that one. Escape outside the editor (in the pane or the list), its ✕, or opening another file ends the difference; in the editor escape stays vim's.

`gsh` (`vimStageHunk`, in the settings' Vim tab beside `guh`) stages the change under the cursor wherever the source pane shows git's changes, the file's unsaved text included, as gitsigns' `stage_hunk` does; in a staged file's difference it unstages it. The `]g` peek has a button for it. Lines within a change can't be staged on their own.

## Committing

A commit commits what is staged, with the box's message; with nothing staged it is refused with a hint, and "Commit all" (a command, never automatic) stages every change, the new files too, before it commits. On a clean tree, with nothing to amend or merge, both buttons are dimmed and do nothing. The repository's hooks run as they do in a shell; when one fails its output is in the git output (below). The message box is a small editor with vim keys, faint rulers at 50 and 72 columns and the subject in amber past 72, and its draft is kept per project until it is committed. `A` puts HEAD's message in the box to amend it, and is refused once HEAD has been pushed (it is in its upstream), since xf never force-pushes.

Discarding asks first, naming the file or how many. A file git doesn't track goes to the system's trash, as the tree's delete does; for a tracked file, a copy of it as it is goes to the trash first and then git puts back the index's version, so a discard can always be undone.

## Pushing and pulling

Pull is `git pull --ff-only`: when the branch and its upstream have both moved on, it fails with git's message and the merge or rebase is the shell's. Push is `git push`, or `git push -u origin <branch>` for a branch with no upstream; sync is a pull and then a push; fetch is the branch switcher's `git fetch --prune origin`. Nothing is ever forced. git is told never to ask for a password or passphrase, which nobody would see: a credential helper or ssh-agent works, and anything else fails with git's message. They are refused while a local run is going, as switching branch is.

One git command that changes something runs at a time per project. While a long one runs (push, pull, fetch, a commit's hooks) the view's header says so, with a ✕ that stops it. A failure shows git's last line of error in red, which opens the **git output**: every command xf ran, with all it printed, as VS Code's Output › Git.

## History, branches and stashes

Recent commits starts open, listing the last 10 commits of HEAD, with their short SHA, subject, author and age, those not yet pushed marked ↑; more loads the next 10. A commit opens to its files, and a file to its difference with the commit's first parent. Nothing reverts, resets or checks out a commit from here.

`n` makes a branch and switches to it (`git switch -c`), from HEAD with the working changes carried along, or from the commit the cursor is on in Recent commits; git decides whether the working changes allow it, as with the branch switcher.

`s` stashes everything, the new files too (`git stash push -u`), with a message if one is typed; so does the Changes header's *stash* button, as Stashes isn't listed until there is a stash. The Stashes section lists the stashes, each opening to its files and their differences; drop asks first, naming the stash.

## Merges, rebases, cherry-picks

While a merge, rebase, cherry-pick or revert is in progress, a banner names it, the files in conflict are listed in Merge Changes on top, and enter opens one in the source pane, its conflict markers to edit. Space marks a file resolved (`git add`). Commit (for a merge) or Continue (the rest) finishes it, and Abort, after asking, runs its `--abort`. Push, pull and stash are refused until it is done. A stash applied or popped with conflicts leaves its files in Merge Changes with no operation to abort, as git leaves it.

## Watching

The view lists git's status again when the tree's watcher sees a file change, and when the index, HEAD, the stash or an operation's files in the git directory change. xf reads the status with `--no-optional-locks`, so its own reads never rewrite the index and wake the watch again.

The TUI has no Source Control view, as it has no file tree.

## Why

With the file tree and the source pane, xf web edits a whole project; committing and pushing the edit was the step that still needed a shell or another editor. The source pane already showed git's changes; staging, committing and pushing them is what VS Code's Source Control does beside its explorer, and lazygit's keys are the ones the TUI's users know.

## Considered options

- **Read-only: the changes and their differences, committing in a shell.** No git writes from a page, but the round trip to a shell stays.
- **Stage and commit, no push or pull.** No credentials or remotes in the browser, but every edit still ends in a shell.
- **A section below the tree, both showing.** Closer to "in the explorer", but both are cramped in one narrow column.
- **Pull as git's config says, or with rebase.** What a shell does, but a pull that stops in a merge or a rebase is a lot to handle in a page.
- **Discard as VS Code does.** Untracked files deleted outright and tracked changes gone; one confirmation stands between a key and lost work.
- **A commit with nothing staged commits everything.** Fast, but it commits stray files.
- **Lines staged on their own.** VS Code's Stage Selected Ranges; building a patch for part of a change is fiddly, and staging a change covers most of it.
- **A graph of the history.** Drawing branches and merges in a narrow column is a lot of work for little.

## Consequences

- xf web writes to git: staging, committing, discarding, stashing, pushing and pulling, all as plain git commands with git's messages, never forced. A change for parity with the TUI must not remove the view (ADR 0004).
- The server reads files from the index, HEAD, commits and stashes (by path, inside the repository) for the differences, and writes the index for `gsh` with `git update-index`.
- The column's view, ctrl+g and the tree's cmd+b are one setting: whether the column shows, and which view.

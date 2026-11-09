---
status: accepted
---

# xf web shows the branch's merge request and its pipeline

xf web shows the current branch's merge request and its last pipeline, on GitLab and on GitHub, where they are a pull request and its workflow runs.
It never writes to GitLab or GitHub: creating a merge request, starting a manual job, retrying or cancelling one all happen on the host's own page, which xf opens.
The TUI doesn't get it (it is in maintenance mode).

## Hosts and tokens

xf calls the hosts' APIs itself, from Go, with a token of its own, as VS Code's GitLab Workflow and GitHub Pull Requests extensions do.
It doesn't run glab or gh.
A read-only token is enough: `read_api` on GitLab; on GitHub a fine-grained token that reads pull requests, actions and checks (or a classic `repo` token for a private repository).

The tokens are in `tokens.json` beside the user's config.json (`$XDG_CONFIG_HOME/xf`, else `~/.config/xf`), written with mode 0600. They never go in config.json, a project's config or the Settings page's JSON view.
`GITLAB_TOKEN` and `GITHUB_TOKEN` in xf's environment win over the file for gitlab.com and github.com, and stand in for a self-hosted host of their kind that has no token in the file (a company's token isn't overridden by one for the public host).

Settings gets a **Git hosting** section: a list of hosts, each with its kind (GitLab or GitHub) and a masked token, a link to the host's page for making a token with the right scopes, and a Test button that says who the token signs in as. github.com and gitlab.com are known without an entry; a self-hosted host, such as a company's GitLab, needs one, since its name doesn't say what it runs.
GitHub Enterprise is an entry of kind GitHub.

The repository is the current branch's push remote (`branch.<name>.pushRemote`, `remote.pushDefault`, the branch's remote, else origin).
An SSH alias in its URL is resolved with `ssh -G`, and the host is matched against the entries.
A host with no match shows nothing.
Only branches in the same project count: a merge request from a fork into another project isn't found.

## The status line

A pill beside `⎇ branch` says where the branch is:

- `⚿ connect`: the host is known but has no token. It opens Settings at Git hosting.
- `+ MR` (`+ PR` on GitHub): the branch has no open merge request. It isn't shown on the base branch.
- `!42 ●` while its pipeline is pending or running, `!42 ✓` when it passed, `!42 ✗` when it failed, `!42 draft` for a draft; `!42 merged` or `!42 closed` for the branch's last merge request once it is no longer open.
  On GitHub it is `#42`.
- `⚿ token` when the host refuses the token. It opens Settings.

A finished pipeline shows as ✓, ✗, ⊘ (cancelled) or ▶ (waiting on a manual job).
When the host can't be reached (a VPN that is off), the pill keeps the last state it knew, dimmed, with "couldn't reach <host> · last checked <when>" as its tooltip.
Clicking the pill opens Source Control at the merge request's section; on `+ MR` it creates one.

## The section in Source Control

Source Control keeps git's part at the top of its pane, as it was; the host's part is a region docked at the bottom, below a sash, as VS Code stacks its views.
Dragging the sash sets the region's height (the same in every project, kept in the browser), at most about 60% of the pane.
The region's header bar names the host (its mark, GitLab or GitHub) and the project, with buttons that open the merge request and ask the host again; folded, it is the header bar alone, showing the pill.
The cursor moves straight from git's rows into the region's (j/k), and tab (or shift+tab) jumps between the two regions, coming back to the row each had; g and G go to the top and bottom of the cursor's region.
The region shows the merge request: its title, its state (draft, open, merged, closed), its target (source → target in its tooltip), approvals and unresolved threads, and what stops it being merged (conflicts, a needed rebase, a failing pipeline, open discussions), in the host's own words.
A draft says so once, as its state: not also as a blocker, nor as the title's `Draft:`, `[Draft]` or `WIP:`, which the row drops.
Below that, the last pipeline's stages and their jobs, each with its state and how long it took; enter on a job opens it on the host.
A manual job shows ▶ and opens on the host too.

When the merge request targets a branch other than xf's base (the one R, I, C and the change markers compare with), the section says so, with a button that makes the target the base, as the base picker does.
Nothing changes the base by itself.

The last pipeline is the merge request's head pipeline, the one the host shows on it; without a merge request, the newest pipeline of the branch.
On GitHub it is the workflow runs of the pull request's head commit, else of the branch's.

When several open merge requests come from the branch, the one that targets the base is shown, else the newest.

## Creating a merge request

Create MR opens the host's new merge request page (GitHub's compare page) in the browser, with the branch as source, the base as target and the last commit's subject as title.
When the branch has no upstream, or has commits its upstream doesn't, xf first asks to push them (as `P` in Source Control does, never forced); Skip opens the page anyway.
The new merge request shows on the next check.

## Keys

The palette has Create MR, Open MR, Open last pipeline, Copy MR link and Refresh MR.
In the merge request's section, `o` opens the merge request, `O` the pipeline and `m` creates one.
There is no new global key.
Open last pipeline with no pipeline says so.

## Checking

xf web's server asks the host, never the page.
It asks when a page opens the project, on push, on a branch switch, on fetch, when a window gets focus back, and on Refresh MR.
While the pipeline is pending or running it asks again every `forgePollSeconds` (a user setting, 10 by default, 5 at least), and stops once it has finished.
When the host can't be reached it backs off, from 30 seconds to 5 minutes, and asks again when a window gets focus.
Only projects that some window shows are asked about; a project that stays open without a window is asked about when one shows it again.

A pipeline that finishes changes the pill and nothing else: no toast, no system notification.

## Why

A branch's work ends in a merge request and its pipeline.
Seeing them meant leaving xf for the host's page, and finding out whether a pipeline passed meant going back to it.
VS Code's extensions show both beside Source Control; xf web had Source Control and push already.

## Considered options

- **Run glab and gh.**
  They hold the sign-in, and a self-hosted host works for free, but xf would depend on two CLIs being installed and logged in.
  The user chose a token of xf's own, as the VS Code extensions have.
- **A token borrowed from glab or gh.** No subprocess per check, but tied to how those tools keep their tokens.
- **The OS keychain.** Encrypted, but WSL and a headless Linux often have no secret service, so a file is needed anyway.
- **Only environment variables.** Nothing stored, but xf web started from Raycast or the app window may not see the shell's environment.
- **A form in xf to create the merge request.** You'd never leave xf, but reviewers, labels and templates are the host's.
- **Job logs, retry and cancel in xf.** The most useful when living in pipelines, but they need a token that writes and a log view; the host's page has them.
- **A toast or a system notification when a pipeline finishes.** The user wants the pill alone.
- **Checking all the time, or every open project.** Catches pipelines others start, but the calls grow with what is open rather than what is looked at.
- **Following the merge request's target as the base.** Always consistent, but the base would change without a pick.
- **Two views behind a git | GitLab switch at the top of Source Control.** Each gets the whole pane, but the pipeline and your changes never show together.
- **A status card above git's sections.** The host's mark and colour set it apart, but unfolded it pushes the changes down.
- **Merge requests from forks.** Right for open-source work, but needs a second remote's lookups; the user's teams work in branches.

## Consequences

- xf keeps secrets for the first time, in a file of their own, in plain text as glab and gh keep theirs.
- A self-hosted host needs an entry in Settings before xf shows anything for it.
- Without a token that writes, the section can show manual jobs but not start them.

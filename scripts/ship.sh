#!/bin/sh
# Ships a committed change: rebases it on origin/main, runs `just check` on
# that exact commit in a clean copy, pushes it to main, fast-forwards the
# main checkout, installs xf from the pushed commit, and removes the
# branch's worktree when invoked from the main checkout.
#
#   scripts/ship.sh [branch]     (or: just ship [branch])
#
# Run from either a worktree or the main checkout:
# - Inside a worktree: ships this worktree's branch (or the named branch) and
#   leaves the active worktree directory intact so the live session keeps its cwd.
# - In the main checkout with a branch: ships that branch's worktree, then
#   removes the worktree and deletes the branch.
# - In the main checkout without a branch: ships main's own commits; another
#   session's uncommitted edits there are left alone and not tested.
#
# The check runs in a detached copy of the commit, so uncommitted edits in the
# main checkout never get in; the push skips the pre-push hook, which would
# only run the same check on the wrong tree.
set -eu

say() { echo "ship: $*" >&2; }
die() { say "$*"; exit 1; }

here=$(git rev-parse --show-toplevel)
main=$(git worktree list --porcelain | sed -n '1s/^worktree //p')
[ "$(git -C "$main" symbolic-ref --short HEAD)" = main ] || die "the main checkout ($main) isn't on main"

branch=${1:-}
if [ "$here" != "$main" ] && [ -z "$branch" ]; then
	branch=$(git symbolic-ref --short HEAD 2>/dev/null) || die "detached HEAD in $here; pass a branch name"
	[ "$branch" != main ] || die "unexpected main branch in worktree $here"
fi

git -C "$main" fetch -q origin main

wt=
if [ -n "$branch" ]; then
	wt=$(git -C "$main" worktree list --porcelain | awk -v b="refs/heads/$branch" '/^worktree /{w=substr($0,10)} $0=="branch "b{print w}')
	[ -n "$wt" ] || die "no worktree has branch $branch"
	[ -z "$(git -C "$wt" status --porcelain --untracked-files=no)" ] || die "$wt has uncommitted changes; commit them first"
	git -C "$wt" rebase -q origin/main || { git -C "$wt" rebase --abort; die "$branch doesn't rebase cleanly on origin/main; rebase it by hand"; }
	sha=$(git -C "$main" rev-parse "refs/heads/$branch")
else
	git -C "$main" merge-base --is-ancestor origin/main HEAD || die "origin/main has commits main lacks; merge them first"
	sha=$(git -C "$main" rev-parse HEAD)
fi
[ "$sha" != "$(git -C "$main" rev-parse origin/main)" ] || die "nothing to ship: origin/main is already at $sha"
say "shipping $(git -C "$main" log --oneline origin/main.."$sha" | wc -l | tr -d ' ') commit(s):"
git -C "$main" log --oneline origin/main.."$sha" >&2

# Every subject a Conventional Commit, as the release notes group by type;
# catches a commit made without the commit-msg hook.
msg=$(mktemp "${TMPDIR:-/tmp}/xf-ship-msg.XXXXXX")
for c in $(git -C "$main" rev-list origin/main.."$sha"); do
	git -C "$main" log -1 --format=%s "$c" >"$msg"
	sh "$main/.githooks/commit-msg" "$msg" || { rm -f "$msg"; die "$(git -C "$main" log -1 --format=%h "$c") needs a Conventional Commit subject; reword it and ship again. Nothing was pushed"; }
done
rm -f "$msg"

# pwd -P: macOS's $TMPDIR is under /var, a link to /private/var, and the
# nvim tests compare the paths they're given with the ones nvim resolves.
tmp=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/xf-ship.XXXXXX")" && pwd -P)
copy=$tmp/xf
cleanup() { git -C "$main" worktree remove --force "$copy" 2>/dev/null || true; rm -rf "$tmp"; }
trap cleanup EXIT
git -C "$main" worktree add -q --detach "$copy" "$sha"

say "just check on $sha (about 30s)"
if ! (cd "$copy" && just check) >"$tmp/check.log" 2>&1; then
	grep -E '^(--- FAIL|FAIL|panic:|gofmt needed:)|_test\.go:[0-9]+:|\.go:[0-9]+:[0-9]+:|^ℹ fail|not ok' "$tmp/check.log" | head -40 >&2 || true
	cp "$tmp/check.log" "$main/.git/ship-check.log"
	die "just check failed; nothing was pushed. Full log: $main/.git/ship-check.log"
fi

git -C "$main" push -q --no-verify origin "$sha:refs/heads/main"
say "pushed $sha to origin/main"

if [ -z "$branch" ]; then
	: # main is the commit shipped
elif git -C "$main" merge -q --ff-only "$sha" 2>"$tmp/merge.err"; then
	say "main checkout fast-forwarded"
else
	say "main checkout NOT fast-forwarded (its uncommitted edits overlap the change):"
	cat "$tmp/merge.err" >&2
	say "once those edits are committed: git merge --ff-only origin/main"
fi

(cd "$copy" && just install) >&2
say "installed xf"

if [ -n "$branch" ]; then
	git -C "$main" merge-base --is-ancestor "$sha" origin/main || die "$sha isn't on origin/main; kept $wt"
	if [ "$here" = "$wt" ]; then
		say "kept active worktree $wt (remove it from main when done: git worktree remove $wt && git branch -D $branch)"
	else
		git -C "$main" worktree remove "$wt"
		git -C "$main" branch -q -D "$branch"
		say "removed $wt and branch $branch"
	fi
fi
say "done: $(git -C "$main" log -1 --format='%h %s' "$sha")"

#!/bin/sh
# Ships a committed change: rebases it on origin/main, runs `just check` on
# that exact commit in a clean copy, pushes it to main, fast-forwards the
# main checkout, installs xf from the pushed commit, and removes the
# thread's worktree and branch.
#
#   scripts/ship.sh [branch]     (or: just ship [branch])
#
# Run it from the main checkout. With a branch, that branch's worktree is
# shipped (commit there first, and leave it: ExitWorktree keep). Without
# one, the main checkout's own commits on main are shipped; another
# session's uncommitted edits there are left alone and not tested.
#
# The check runs in a detached copy of the commit, so the main checkout's
# uncommitted edits never get in; the push then skips the pre-push hook,
# which would only run the same check on the wrong tree.
set -eu

say() { echo "ship: $*" >&2; }
die() { say "$*"; exit 1; }

main=$(git worktree list --porcelain | sed -n '1s/^worktree //p')
[ "$(git rev-parse --show-toplevel)" = "$main" ] || die "run me from the main checkout ($main); in a worktree, commit, then ExitWorktree keep"
[ "$(git -C "$main" symbolic-ref --short HEAD)" = main ] || die "the main checkout isn't on main"

branch=${1:-}
git fetch -q origin main

if [ -n "$branch" ]; then
	wt=$(git worktree list --porcelain | awk -v b="refs/heads/$branch" '/^worktree /{w=substr($0,10)} $0=="branch "b{print w}')
	[ -n "$wt" ] || die "no worktree has branch $branch"
	[ -z "$(git -C "$wt" status --porcelain --untracked-files=no)" ] || die "$wt has uncommitted changes; commit them first"
	git -C "$wt" rebase -q origin/main || { git -C "$wt" rebase --abort; die "$branch doesn't rebase cleanly on origin/main; rebase it by hand"; }
	sha=$(git rev-parse "refs/heads/$branch")
else
	git merge-base --is-ancestor origin/main HEAD || die "origin/main has commits main lacks; merge them first"
	sha=$(git rev-parse HEAD)
fi
[ "$sha" != "$(git rev-parse origin/main)" ] || die "nothing to ship: origin/main is already at $sha"
say "shipping $(git log --oneline origin/main.."$sha" | wc -l | tr -d ' ') commit(s):"
git log --oneline origin/main.."$sha" >&2

# Every subject a Conventional Commit, as the release notes group by type;
# catches a commit made without the commit-msg hook.
msg=$(mktemp "${TMPDIR:-/tmp}/xf-ship-msg.XXXXXX")
for c in $(git rev-list origin/main.."$sha"); do
	git log -1 --format=%s "$c" >"$msg"
	sh "$main/.githooks/commit-msg" "$msg" || { rm -f "$msg"; die "$(git log -1 --format=%h "$c") needs a Conventional Commit subject; reword it and ship again. Nothing was pushed"; }
done
rm -f "$msg"

# pwd -P: macOS's $TMPDIR is under /var, a link to /private/var, and the
# nvim tests compare the paths they're given with the ones nvim resolves.
tmp=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/xf-ship.XXXXXX")" && pwd -P)
copy=$tmp/xf
cleanup() { git worktree remove --force "$copy" 2>/dev/null || true; rm -rf "$tmp"; }
trap cleanup EXIT
git worktree add -q --detach "$copy" "$sha"

say "just check on $sha (about 30s)"
if ! (cd "$copy" && just check) >"$tmp/check.log" 2>&1; then
	grep -E '^(--- FAIL|FAIL|panic:)|_test\.go:[0-9]+:|\.go:[0-9]+:[0-9]+:|^ℹ fail|not ok' "$tmp/check.log" | head -40 >&2 || true
	cp "$tmp/check.log" "$main/.git/ship-check.log"
	die "just check failed; nothing was pushed. Full log: $main/.git/ship-check.log"
fi

git push -q --no-verify origin "$sha:refs/heads/main"
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
if [ -d "$HOME/.local/bin" ]; then
	# rm first: a cp over the old file leaves a binary macOS kills on launch.
	rm -f "$HOME/.local/bin/xf"
	cp "$HOME/go/bin/xf" "$HOME/.local/bin/xf"
	"$HOME/.local/bin/xf" cost --help >/dev/null 2>&1 || die "the new ~/.local/bin/xf doesn't run"
	say "installed xf in ~/go/bin and ~/.local/bin"
fi

if [ -n "$branch" ]; then
	git merge-base --is-ancestor "$sha" origin/main || die "$sha isn't on origin/main; kept $wt"
	git worktree remove "$wt"
	git branch -q -D "$branch"
	say "removed $wt and branch $branch"
fi
say "done: $(git log -1 --format='%h %s' "$sha")"

#!/bin/sh
# Pre-release check on HEAD (or a given ref): runs `just release-check`,
# builds a GoReleaser snapshot in a clean detached worktree (so dirty files in
# the working tree never interfere), reports Windows CI status for that commit,
# and prints the XF_SKIP_WINGET repository variable.
#
#   scripts/release-preflight.sh [ref]     (or: just release-preflight)
set -eu

repo_slug="ashish10alex/xf"
ref=${1:-HEAD}
sha=$(git rev-parse "$ref")
short=$(git rev-parse --short "$sha")

echo "== release-check" >&2
just release-check

echo "== release-snapshot on clean $short" >&2
tmp=$(cd "$(mktemp -d "${TMPDIR:-/tmp}/xf-release-preflight.XXXXXX")" && pwd -P)
copy=$tmp/xf
cleanup() { git worktree remove --force "$copy" 2>/dev/null || true; rm -rf "$tmp"; }
trap cleanup EXIT
git worktree add -q --detach "$copy" "$sha"
(cd "$copy" && just release-snapshot)
echo "== snapshot artifacts" >&2
ls -la "$copy/dist/" >&2

if command -v gh >/dev/null 2>&1; then
	echo "== Windows CI for $short ($repo_slug)" >&2
	gh run list -R "$repo_slug" --workflow windows.yml --commit "$sha" --limit 3 >&2 || true
	skip=$(gh variable get XF_SKIP_WINGET -R "$repo_slug" 2>/dev/null || echo "<unset>")
	echo "== XF_SKIP_WINGET=$skip" >&2
else
	echo "== gh not found; check Windows CI and XF_SKIP_WINGET manually" >&2
fi

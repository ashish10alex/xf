#!/bin/sh
# Starts xf web on a scratch copy of an example project, for the browser
# preview (.claude/launch.json) and manual checks. It builds xf from this
# checkout, serves the page's assets from the checkout (so JS and CSS edits
# show on a reload, with no rebuild), and keeps xf's config and state in the
# scratch dir, away from the user's own.
#
#   scripts/preview.sh [project] [port]
#
# project is a name under examples/ (default dataform) or a directory. The copy
# is a git repo with one commit, so source control, branches and hunks work.
# It is kept between starts, with its edits and config; XF_PREVIEW_FRESH=1
# makes a new one. Settings go in <dir>/xdg/xf/config.json, for example
# {"sourceAutosaveMs": 500}. XF_PREVIEW_BIN runs that xf instead of a build.
# XF_PREVIEW_EMBEDDED=1 serves the assets built into xf, as a release does
# (to measure loads), instead of the checkout's.
#
# launch.json runs this with Homebrew's bash: when the desktop app starts it,
# macOS won't let /bin/sh read a script under ~/Documents.
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd -P)
project=${1:-dataform}
port=${2:-7900}
case $project in
/*) src=$project name=$(basename "$project") ;;
*) src=$repo/examples/$project name=$project ;;
esac
if [ ! -d "$src" ]; then
	echo "preview: no project at $src" >&2
	exit 1
fi

tmp=${TMPDIR:-/tmp}
dir=${XF_PREVIEW_DIR:-${tmp%/}/xf-preview}/$name-$port
work=$dir/$name
if [ "${XF_PREVIEW_FRESH:-}" = 1 ]; then
	rm -rf "$dir"
fi
mkdir -p "$dir/xdg" "$dir/state"

bin=${XF_PREVIEW_BIN:-}
if [ -z "$bin" ]; then
	bin=$dir/xf
	(cd "$repo" && go build -o "$bin" ./cmd/xf)
fi

if [ ! -d "$work" ]; then
	cp -R "$src" "$work"
	rm -rf "$work/.git"
	(
		cd "$work"
		git init -q
		git add -A
		git -c user.name=xf -c user.email=xf@example.invalid commit -qm "$name example"
	)
fi

assets=$repo/internal/web/assets
if [ "${XF_PREVIEW_EMBEDDED:-}" = 1 ]; then
	assets=
fi
echo "preview: xf from $repo, assets from ${assets:-the build}" >&2
echo "preview: $work, config in $dir/xdg/xf; open /?project=$name" >&2
# cd first: xf asking for a working directory under ~/Documents can hang on
# macOS's privacy check when the app starts it.
cd "$work"
exec env XDG_CONFIG_HOME="$dir/xdg" XDG_STATE_HOME="$dir/state" \
	XF_WEB_ASSETS="$assets" \
	"$bin" web --no-open --port "$port" --dir "$work"

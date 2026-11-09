#!/bin/sh
# Starts scripts/preview.sh in the background on a free port and waits until
# xf web answers, then prints its URL. For a preview of this checkout when
# preview_start can't give one (a worktree: the app runs the main checkout's
# launch.json in the main checkout). Open the URL with preview_start {url}.
#
#   scripts/preview-bg.sh [project]       (dataform by default)
#   scripts/preview-bg.sh stop <port>
#
# The log is beside the scratch dir, as <dir>.log; its first lines name the dir.
set -eu
here=$(cd "$(dirname "$0")" && pwd -P)

if [ "${1:-}" = stop ]; then
	pids=$(lsof -t -nP -iTCP:"${2:?port}" -sTCP:LISTEN || true)
	[ -n "$pids" ] && kill $pids && echo "stopped the preview on ${2}"
	exit 0
fi

project=${1:-dataform}
port=7910
while lsof -nP -iTCP:$port -sTCP:LISTEN >/dev/null 2>&1; do
	port=$((port + 1))
	[ $port -le 7999 ] || { echo "preview-bg: no free port in 7910-7999" >&2; exit 1; }
done

name=$(basename "$project")
tmp=${TMPDIR:-/tmp}
base=${XF_PREVIEW_DIR:-${tmp%/}/xf-preview}
mkdir -p "$base"
# Beside the scratch dir, not in it: XF_PREVIEW_FRESH=1 removes that.
log=$base/$name-$port.log
nohup /opt/homebrew/bin/bash "$here/preview.sh" "$project" "$port" >"$log" 2>&1 &
pid=$!

i=0
until curl -s -o /dev/null "http://127.0.0.1:$port/"; do
	if ! kill -0 $pid 2>/dev/null; then
		echo "preview-bg: preview.sh exited:" >&2
		tail -20 "$log" >&2
		exit 1
	fi
	i=$((i + 1))
	[ $i -le 180 ] || { echo "preview-bg: no answer on $port after 90s; log: $log" >&2; exit 1; }
	sleep 0.5
done
echo "http://127.0.0.1:$port/?project=$name"
echo "(pid $pid, log $log; stop with: scripts/preview-bg.sh stop $port)"

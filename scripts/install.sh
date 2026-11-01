#!/bin/sh
# Installs the latest xf release on macOS or Linux:
#
#   curl -fsSL https://raw.githubusercontent.com/ashish10alex/xf/main/scripts/install.sh | sh
#
# XF_VERSION picks a release (e.g. v0.0.1) instead of the latest, and
# XF_INSTALL_DIR the folder xf goes in (default ~/.local/bin). The archive
# is checked against the release's checksums.txt before anything is
# installed.
set -eu

repo=ashish10alex/xf
dir=${XF_INSTALL_DIR:-$HOME/.local/bin}

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) echo "xf: unsupported OS $(uname -s); on Windows use scripts/install.ps1" >&2; exit 1 ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "xf: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${XF_VERSION:-}" ]; then
    base=https://github.com/$repo/releases/download/$XF_VERSION
else
    base=https://github.com/$repo/releases/latest/download
fi
archive=xf_${os}_${arch}.tar.gz

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$archive" "$base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
    got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
    echo "xf: $archive doesn't match checksums.txt; nothing installed" >&2
    exit 1
fi

tar -xzf "$tmp/$archive" -C "$tmp" xf
mkdir -p "$dir"
# Moved into place rather than written over, so a running xf keeps its
# file and macOS doesn't kill the new one for a stale code signature.
mv -f "$tmp/xf" "$dir/xf"
echo "installed $dir/xf"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "add $dir to your PATH to run xf" ;;
esac

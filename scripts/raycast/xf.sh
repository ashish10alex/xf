#!/bin/bash

# A Raycast script command (macOS): typing "xf" in Raycast brings the xf web
# window you used last to the front, or starts `xf web --app` when none is
# open. See "Back to xf web from Raycast" in docs/xf-web.md.

# Required parameters:
# @raycast.schemaVersion 1
# @raycast.title xf
# @raycast.mode silent

# Optional parameters:
# @raycast.icon 🗂
# @raycast.packageName xf
# @raycast.description Bring the xf web window to the front, or start xf web --app

# The xf binaries this command is for, separated by ":": it brings forward
# only windows whose xf web runs one of them, and starts the first that
# exists. Empty: any xf window, and the xf on your login shell's PATH. A
# second command, for a nightly build say, can set it and run this script:
#   XF_BINS=~/go/bin/xf exec "$(dirname "$0")/xf.sh"
bins=${XF_BINS:-}

log=~/Library/Logs/xf-raycast.log
echo "--- $(date '+%F %T') bins=${bins:-any}" >> "$log"

# ours: does the xf web listening on port run one of $bins?
ours() {
  [ -z "$bins" ] && return 0
  local pid exe b
  pid=$(lsof -nP -iTCP:"$1" -sTCP:LISTEN -t 2>/dev/null | head -1)
  [ -n "$pid" ] || return 1
  # A process's first txt file is its executable, with links resolved.
  exe=$(lsof -a -p "$pid" -d txt -Fn 2>/dev/null | sed -n 's/^n//p' | head -1)
  IFS=: read -ra list <<< "$bins"
  for b in "${list[@]}"; do
    [ -e "$b" ] && [ "$(realpath "$b")" = "$exe" ] && return 0
  done
  return 1
}

# Chrome's windows, front to back, as "id<TAB>url<TAB>title" lines.
windows=$(osascript 2>>"$log" <<'EOF'
if application "Google Chrome" is not running then return ""
set out to ""
-- Inside the Chrome tell block, "tab" is a browser tab, not the character.
set sep to character id 9
tell application "Google Chrome"
  repeat with w in windows
    set out to out & (id of w) & sep & (URL of active tab of w) & sep & (title of w) & linefeed
  end repeat
end tell
return out
EOF
)
status=$?
printf 'osascript exit %s, windows:\n%s\n' "$status" "$windows" >> "$log"
if [ "$status" -ne 0 ]; then
  # Without Chrome's windows, starting xf web could make yet another copy.
  echo "Can't read Chrome's windows: allow Raycast in System Settings, Privacy & Security, Automation"
  exit 1
fi

# Bring forward the frontmost xf window whose xf web still answers. Its
# title is "xf" (the project list) or "xf · <project>", maybe with a "● "
# in front; a window whose xf web has stopped is skipped.
while IFS=$'\t' read -r id url title; do
  [[ "$title" =~ ^(●\ )?xf($|\ ·\ ) ]] || continue
  [[ "$url" == http://127.0.0.1:* ]] || continue
  if ! curl -s -o /dev/null --noproxy '*' --max-time 1 "$url"; then
    echo "skip $url: xf web does not answer" >> "$log"
    continue
  fi
  port=${url#http://127.0.0.1:}; port=${port%%/*}
  if ! ours "$port"; then
    echo "skip $url: not one of $bins" >> "$log"
    continue
  fi
  echo "focus $id $url" >> "$log"
  osascript -e "tell application \"Google Chrome\"" \
    -e "set index of window id $id to 1" \
    -e "activate" \
    -e "end tell" >/dev/null
  exit 0
done <<< "$windows"

# None: start xf web in an app window (it opens your project list).
# Raycast gives scripts a bare PATH, so take the login shell's, which has
# xf, dataform and node (nvm, say), gcloud and dbt.
shell_path=$(/bin/zsh -lic 'printf "\nXFPATH=%s\n" "$PATH"' 2>/dev/null | sed -n 's/^XFPATH=//p' | tail -1)
[ -n "$shell_path" ] && export PATH="$shell_path"
xf=
if [ -n "$bins" ]; then
  IFS=: read -ra list <<< "$bins"
  for b in "${list[@]}"; do
    [ -x "$b" ] && { xf=$b; break; }
  done
  [ -n "$xf" ] || { echo "None of $bins exists"; exit 1; }
else
  xf=$(command -v xf) || { echo "xf isn't on your shell's PATH"; exit 1; }
fi
echo "start $xf web" >> "$log"
nohup "$xf" web --app >> "$log" 2>&1 &

#!/bin/bash

# Keeps `xf web` running on a fixed port (macOS), so the xf app that Chrome
# installs from the page ("Install page as app") opens at once: that app is a
# window onto 127.0.0.1:<port>, and cannot start xf web itself. See "xf as a
# Mac app" in the README.
#
#   xf-web-agent.sh install [port]   write and start the agent (default port 7777)
#   xf-web-agent.sh uninstall        stop and remove it
#   xf-web-agent.sh print [port]     show the plist without installing it

set -euo pipefail

label=dev.xf.web
plist=~/Library/LaunchAgents/$label.plist
log=~/Library/Logs/xf-web.log

xf_bin=$(command -v xf || true)
cmd=${1:-}
port=${2:-7777}

# launchd gives an agent a bare PATH, so give it the login shell's, which has
# dataform, node (nvm, say), gcloud and dbt.
make_plist() {
  local shell_path
  shell_path=$(/bin/zsh -lic 'printf "\nXFPATH=%s\n" "$PATH"' 2>/dev/null | sed -n 's/^XFPATH=//p' | tail -1)
  cat <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key>
  <array>
    <string>$xf_bin</string>
    <string>web</string>
    <string>--port</string><string>$port</string>
    <string>--no-open</string>
    <string>--web-auto-exit-seconds</string><string>0</string>
  </array>
  <key>WorkingDirectory</key><string>$HOME</string>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>${shell_path:-/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin}</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>$log</string>
  <key>StandardErrorPath</key><string>$log</string>
</dict>
</plist>
PLIST
}

need_xf() {
  [ -n "$xf_bin" ] || { echo "xf isn't on PATH: install it first (README, 'Install xf')" >&2; exit 1; }
}

case "$cmd" in
  install)
    need_xf
    mkdir -p ~/Library/LaunchAgents ~/Library/Logs
    # Stop an earlier copy first, so a changed port or path takes effect.
    launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true
    make_plist > "$plist"
    plutil -lint "$plist" >/dev/null
    launchctl bootstrap "gui/$(id -u)" "$plist"
    echo "xf web runs on http://127.0.0.1:$port (log: $log)"
    ;;
  uninstall)
    launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true
    rm -f "$plist"
    echo "removed $label"
    ;;
  print)
    need_xf
    make_plist
    ;;
  *)
    echo "usage: $(basename "$0") install [port] | uninstall | print [port]" >&2
    exit 2
    ;;
esac

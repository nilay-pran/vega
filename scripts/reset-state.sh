#!/usr/bin/env bash
# Wipe the daemon's per-user state (socket, token, SQLite db) so the next run
# starts clean. On macOS, also unload the installed LaunchAgent if present.
#
#   scripts/reset-state.sh
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib.sh

DIR="$(state_dir)"

if [ "$(uname -s)" = "Darwin" ]; then
  PLIST=/Library/LaunchAgents/ke.sli.slike.uploaderd.plist
  if [ -f "$PLIST" ]; then
    echo "==> unloading LaunchAgent"
    launchctl bootout "gui/$(id -u)" "$PLIST" 2>/dev/null || true
  fi
  rm -f /tmp/vegad.log
fi

# Kill any stray daemon still holding the socket.
pkill -f 'uploaderd' 2>/dev/null || true

echo "==> removing state dir: $DIR"
rm -rf "$DIR"
echo "done."

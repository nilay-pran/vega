#!/usr/bin/env bash
# Shared helpers for the dev scripts. Source this; don't run it directly.

# Repo root, independent of where a script is invoked from.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# state_dir mirrors os.UserConfigDir()/vega — where the daemon and the
# desktop app both keep the control socket, bearer token, and SQLite state.
state_dir() {
  case "$(uname -s)" in
    Darwin) echo "$HOME/Library/Application Support/vega" ;;
    *)      echo "${XDG_CONFIG_HOME:-$HOME/.config}/vega" ;;
  esac
}

# read_token returns the shared bearer token the daemon generated on first run.
read_token() {
  cat "$(state_dir)/token"
}

# wait_for waits up to N seconds for a file (e.g. the control socket) to appear.
wait_for() {
  local path="$1" tries="${2:-50}"
  while [ "$tries" -gt 0 ]; do
    [ -e "$path" ] && return 0
    sleep 0.2
    tries=$((tries - 1))
  done
  echo "timed out waiting for $path" >&2
  return 1
}

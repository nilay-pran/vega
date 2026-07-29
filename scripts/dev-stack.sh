#!/usr/bin/env bash
# Run the full local stack for interactive development: one upload-server that
# multiplexes HTTPS (API + uploads) and SLKT on a single port, plus the
# uploaderd daemon on the DEFAULT per-user socket so the desktop app / CLI
# attach unchanged. Ctrl-C stops both.
#
#   scripts/dev-stack.sh
#
# The server binds a non-privileged port (production uses 443) with a self-signed
# dev certificate, so the daemon connects with -insecure. The UI starts its own
# daemon automatically, so for the desktop app you normally just run it directly;
# this stack is for CLI / server iteration.
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib.sh

ADDR="${ADDR:-:8443}"
SERVER_URL="${SERVER_URL:-https://localhost:8443}"

pids=()
cleanup() {
  echo; echo "stopping stack..."
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup INT TERM EXIT

echo "==> upload-server (mem backend, HTTPS + SLKT multiplexed) on $ADDR"
go run ./apps/upload-server/cmd/upload-server -addr "$ADDR" -backend mem &
pids+=($!)

# Give the server a moment to bind, then start the daemon against it. auto
# transport probes SLKT (same port) first and falls back to HTTPS; -insecure
# trusts the server's self-signed dev certificate.
sleep 1
echo "==> uploaderd (auto transport -> $SERVER_URL) on the default socket"
echo "    socket: $(state_dir)/uploaderd.sock"
go run ./apps/uploaderd -transport auto -server "$SERVER_URL" -insecure &
pids+=($!)

echo "==> stack up. Ctrl-C to stop. Attach the UI with: (cd apps/uploader-desktop && wails3 task dev)"
wait

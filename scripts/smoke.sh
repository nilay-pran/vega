#!/usr/bin/env bash
# Headless end-to-end smoke test: bring up the server + daemon on an ISOLATED
# dev socket/db (so it never touches your real state), enqueue a generated file
# over the control socket, and assert it reaches "completed". Cleans up after.
#
#   scripts/smoke.sh [SIZE_MB]   (default 10)
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib.sh

SIZE_MB="${1:-10}"
DEV="$(mktemp -d)"
SOCK="$DEV/uploaderd.sock"
ADDR=":7080"
SERVER_URL="http://localhost:7080"

pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$DEV"
}
trap cleanup EXIT

echo "==> upload-server (mem) on $ADDR"
go run ./apps/upload-server/cmd/upload-server -addr "$ADDR" -backend mem >"$DEV/server.log" 2>&1 &
pids+=($!)
sleep 1

echo "==> uploaderd (http) on isolated socket $SOCK"
go run ./apps/uploaderd -transport http -server "$SERVER_URL" \
  -socket "$SOCK" -db "$DEV/uploader.db" >"$DEV/daemon.log" 2>&1 &
pids+=($!)
wait_for "$SOCK"

TOK="$(read_token)"
curl_ipc() { curl -fsS --unix-socket "$SOCK" -H "Authorization: Bearer $TOK" "$@"; }

echo "==> generating ${SIZE_MB}MiB test file"
FILE="$DEV/sample.bin"
dd if=/dev/urandom of="$FILE" bs=1048576 count="$SIZE_MB" 2>/dev/null

echo "==> enqueue over the control socket"
ID="$(curl_ipc -H 'Content-Type: application/json' \
      -d "{\"path\":\"$FILE\"}" "http://localhost/v1/uploads" \
      | sed -E 's/.*"id":"([^"]+)".*/\1/')"
echo "    upload id: $ID"

echo "==> polling for completion"
for _ in $(seq 1 100); do
  STATUS="$(curl_ipc "http://localhost/v1/uploads/$ID" \
            | sed -E 's/.*"status":"([^"]+)".*/\1/')"
  case "$STATUS" in
    completed) echo "==> PASS: upload completed"; exit 0 ;;
    failed)    echo "==> FAIL: upload failed"; curl_ipc "http://localhost/v1/uploads/$ID"; echo; exit 1 ;;
  esac
  sleep 0.5
done

echo "==> FAIL: timed out (last status: ${STATUS:-none})"
tail -n 20 "$DEV/daemon.log" >&2
exit 1

#!/usr/bin/env bash
set -euo pipefail

USERDATA_DIR="${CHROMIUM_USER_DATA_DIR:-/home/chromiumuser/user-data}"
REMOTE_DEBUG_PORT="${REMOTE_DEBUG_PORT:-9222}"
LISTEN_ADDR="${LISTEN_ADDR:-:9223}"
CHROMIUM_REMOTE_DEBUGGING_URL="${CHROMIUM_REMOTE_DEBUGGING_URL:-http://127.0.0.1:${REMOTE_DEBUG_PORT}}"

mkdir -p "${USERDATA_DIR}"

cleanup() {
  if [[ -n "${CHROMIUM_PID:-}" ]]; then
    kill "$CHROMIUM_PID" 2>/dev/null || true
  fi
  if [[ -n "${PROXY_PID:-}" ]]; then
    kill "$PROXY_PID" 2>/dev/null || true
  fi
}

trap cleanup SIGINT SIGTERM

chromium-proxy --chromium "${CHROMIUM_REMOTE_DEBUGGING_URL}" --listen "${LISTEN_ADDR}" &
PROXY_PID=$!

chromium \
  --headless \
  --disable-gpu \
  --disable-dev-shm-usage \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port="${REMOTE_DEBUG_PORT}" \
  --disable-background-networking \
  --user-data-dir="${USERDATA_DIR}" \
  --disable-features=VizDisplayCompositor &
CHROMIUM_PID=$!

wait -n "$PROXY_PID" "$CHROMIUM_PID"
STATUS=$?

cleanup

wait || true

exit "$STATUS"

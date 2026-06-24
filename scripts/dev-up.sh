#!/usr/bin/env bash
# One-command local launch so you can actually PLAY the game in a browser.
#
# Starts:
#   - the Go backend (WebTransport :4433, HTTP :8777) — in-memory mode, no infra
#     needed, seeded with a sample 5x5 board
#   - the three Vite dev servers: stage :8778, admin :8779, mobile :8780
#
# Then prints the URLs to open. Ctrl-C tears everything down.
#
# Requirements: Go, Node/npm, and a Chromium-based browser (Chrome/Edge/Brave)
# for WebTransport. Safari/Firefox do NOT support WebTransport yet.
#
# Audio: without Spotify creds the stage runs in "demo mode" (mock player) — the
# full game loop, animations, buzzer, scoring and grading all work; you just
# won't hear music. To wire real audio see deploy/.env.example + the stage README.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Load Spotify creds + secrets from deploy/.env if present (not committed).
# This is what lets real audio work via the launcher; without it the stage
# falls back to demo mode.
if [ -f "${ROOT}/deploy/.env" ]; then
  echo "==> loading ${ROOT}/deploy/.env"
  set -a; . "${ROOT}/deploy/.env"; set +a
fi

ADMIN_SECRET="${ADMIN_SECRET:-letmein}"
# 127.0.0.1 (NOT localhost): Spotify requires loopback redirect URIs to use
# 127.0.0.1, and the browser host must match the registered redirect exactly.
HOST="127.0.0.1"

# Ports
HTTP_PORT=8777
WT_PORT=4433
STAGE_PORT=8778
ADMIN_PORT=8779
MOBILE_PORT=8780

PIDS=()
cleanup() {
  echo ""
  echo "==> shutting down"
  for pid in "${PIDS[@]}"; do kill "$pid" >/dev/null 2>&1 || true; done
  wait >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# Endpoints the browser clients use (passed as Vite env).
export VITE_WT_URL="https://${HOST}:${WT_PORT}/wt"
export VITE_HTTP_URL="http://${HOST}:${HTTP_PORT}"
export VITE_JOIN_URL="http://${HOST}:${MOBILE_PORT}"   # stage QR -> mobile buzzer
# Stage authenticates to the backend (hello + /api/spotify/token) with this.
export VITE_STAGE_SECRET="${ADMIN_SECRET}"

echo "==> building + starting Go backend (in-memory, sample board)"
[ -n "${SPOTIFY_CLIENT_ID:-}" ] && echo "    Spotify creds present — real audio available" \
  || echo "    no Spotify creds — stage runs in demo mode (no audio)"
cd "${ROOT}/server"
ADMIN_SECRET="${ADMIN_SECRET}" \
YFI_HTTP_ADDR=":${HTTP_PORT}" \
YFI_LISTEN_ADDR=":${WT_PORT}" \
YFI_CERT_FILE="${ROOT}/certs/cert.pem" \
YFI_KEY_FILE="${ROOT}/certs/key.pem" \
SPOTIFY_CLIENT_ID="${SPOTIFY_CLIENT_ID:-}" \
SPOTIFY_CLIENT_SECRET="${SPOTIFY_CLIENT_SECRET:-}" \
SPOTIFY_REDIRECT_URI="${SPOTIFY_REDIRECT_URI:-http://${HOST}:${HTTP_PORT}/auth/spotify/callback}" \
  go run ./cmd/gameserver &
PIDS+=($!)

# Wait for the backend HTTP health endpoint.
echo -n "==> waiting for backend"
for _ in $(seq 1 30); do
  if curl -sf "http://${HOST}:${HTTP_PORT}/healthz" >/dev/null 2>&1; then break; fi
  echo -n "."; sleep 0.5
done
echo " ready"

start_web() {
  local app="$1" port="$2"
  local dir="${ROOT}/web/${app}"
  # Always run install: it's a fast no-op when satisfied, but crucially it pulls
  # deps that were ADDED to package.json since the last run (e.g. after a merge).
  # The old "only if node_modules missing" check let new deps go uninstalled,
  # which is exactly the @hello-pangea/dnd break we hit. Fail loudly if it can't.
  echo "==> ensuring deps for web/${app}"
  if ! (cd "${dir}" && npm install >/dev/null 2>&1); then
    echo "ERROR: npm install failed for web/${app}" >&2
    exit 1
  fi
  echo "==> starting web/${app} on :${port}"
  (cd "${dir}" && npm run dev -- --port "${port}" --strictPort >/dev/null 2>&1) &
  PIDS+=($!)
}

start_web stage  "${STAGE_PORT}"
start_web admin  "${ADMIN_PORT}"
start_web mobile "${MOBILE_PORT}"

sleep 2
cat <<EOF

============================================================
  yfitops V2 is up.  Open these in Chrome/Edge/Brave:
------------------------------------------------------------
  STAGE  (projector / big screen) : http://${HOST}:${STAGE_PORT}
  ADMIN  (control room)           : http://${HOST}:${ADMIN_PORT}
  MOBILE (buzzer)                 : http://${HOST}:${MOBILE_PORT}

  Admin password (ADMIN_SECRET)   : ${ADMIN_SECRET}
============================================================

  Try this:
    1. Open ADMIN, log in with the password above.
    2. Open STAGE — you'll see the board + a join QR.
    3. Open MOBILE (or scan the QR with your phone on the
       same Wi-Fi), enter a handle, Join.
    4. In ADMIN, click a board cell -> STAGE runs the
       decryption animation + point timer, MOBILE shows GUESS.
    5. Tap GUESS on mobile -> STAGE freezes, ADMIN shows the
       buzz + correct answer -> grade Correct/Partial/Incorrect.

  No sound = expected (demo mode, no Spotify creds).
  Ctrl-C here stops everything.

EOF

wait

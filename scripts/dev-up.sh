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

# Logs: each service writes here so a death is visible (not swallowed by
# /dev/null). Tail any of these if something doesn't come up.
LOG_DIR="${ROOT}/scripts/_work/logs"
mkdir -p "${LOG_DIR}"

PIDS=()
cleanup() {
  echo ""
  echo "==> shutting down"
  for pid in "${PIDS[@]}"; do kill "$pid" >/dev/null 2>&1 || true; done
  wait >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# Free a TCP port before we bind it. Vite runs with --strictPort (so it fails
# fast rather than silently hopping to another port), which means a leftover
# process from a previous run would kill the new one. Clear it first.
free_port() {
  local p="$1"
  local pids
  pids=$(lsof -ti tcp:"${p}" 2>/dev/null || true)
  if [ -n "${pids}" ]; then
    echo "==> port ${p} busy — clearing (pids: ${pids})"
    echo "${pids}" | xargs kill -9 2>/dev/null || true
    sleep 1
  fi
}

# If a previous dev-up.sh is still running, stop it (and its children) so this
# launch cleanly replaces it instead of colliding on ports.
SELF_PID=$$
PRIOR=$(pgrep -f "dev-up.sh" 2>/dev/null | grep -v "^${SELF_PID}$" || true)
if [ -n "${PRIOR}" ]; then
  echo "==> a previous dev-up is running (pids: ${PRIOR}) — stopping it"
  # Kill each prior launcher's process group so its child servers die too.
  for pid in ${PRIOR}; do
    kill -- "-$(ps -o pgid= "${pid}" 2>/dev/null | tr -d ' ')" 2>/dev/null || kill "${pid}" 2>/dev/null || true
  done
  sleep 1
fi

echo "==> pre-clearing ports"
for p in "${HTTP_PORT}" "${WT_PORT}" "${STAGE_PORT}" "${ADMIN_PORT}" "${MOBILE_PORT}"; do
  free_port "${p}"
done

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
  go run ./cmd/gameserver > "${LOG_DIR}/backend.log" 2>&1 &
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
  echo "==> starting web/${app} on :${port} (log: ${LOG_DIR}/${app}.log)"
  (cd "${dir}" && npm run dev -- --port "${port}" --strictPort > "${LOG_DIR}/${app}.log" 2>&1) &
  PIDS+=($!)
}

start_web stage  "${STAGE_PORT}"
start_web admin  "${ADMIN_PORT}"
start_web mobile "${MOBILE_PORT}"

# Verify each frontend actually came up; report (don't silently continue) if not.
echo "==> verifying frontends"
for pair in "stage:${STAGE_PORT}" "admin:${ADMIN_PORT}" "mobile:${MOBILE_PORT}"; do
  app="${pair%%:*}"; port="${pair##*:}"
  up=0
  for _ in $(seq 1 20); do
    if curl -sf -o /dev/null "http://${HOST}:${port}/"; then up=1; break; fi
    sleep 0.5
  done
  if [ "${up}" -eq 1 ]; then
    echo "    web/${app} :${port} ✅"
  else
    echo "    web/${app} :${port} ❌ NOT responding — last log lines:"
    tail -8 "${LOG_DIR}/${app}.log" 2>/dev/null | sed 's/^/        /'
  fi
done

sleep 1
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
    1. Open STAGE first (leave it open) — board + join QR.
    2. Open ADMIN, log in with the password above.
    3. Open MOBILE (or scan the QR with your phone on the
       same Wi-Fi), enter a handle, Join.
    4. In ADMIN, click a board cell -> STAGE runs the
       decryption animation + point timer, MOBILE shows GUESS.
    5. Tap GUESS on mobile -> STAGE freezes, ADMIN shows the
       buzz + correct answer -> grade Correct/Partial/Incorrect.
EOF

# Spotify status: creds present is NOT the same as authenticated.
if [ -n "${SPOTIFY_CLIENT_ID:-}" ]; then
  cat <<EOF
  AUDIO: Spotify creds loaded, but NOT yet authenticated. To enable sound:
    - In ADMIN, click "Connect Spotify" and complete login.
    - The STAGE browser must be logged into a Spotify PREMIUM account.
    - Until then the stage uses the mock player (no audio).
EOF
else
  cat <<EOF
  AUDIO: no Spotify creds in deploy/.env — stage runs in demo mode (no sound).
    Add SPOTIFY_CLIENT_ID / SPOTIFY_CLIENT_SECRET to enable real audio.
EOF
fi

cat <<EOF

  Logs: ${LOG_DIR}/{backend,stage,admin,mobile}.log
  Ctrl-C here stops everything.

EOF

wait

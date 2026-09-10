#!/usr/bin/env bash
# temp/ws_matrix.sh — live proof of the /ws registration matrix + prod boot gate.
# Boots the real gameserver binary in-memory (no Redis/PG) like qa/smoke.sh and
# probes the actual route with a real WebSocket upgrade request.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}/server" || exit 1
BIN="${ROOT}/temp/yfi_gs_matrix"   # scratch build target; temp/ is gitignored
go build -o "$BIN" ./cmd/gameserver || exit 1

HTTP=18791
QUIC=14491

run_case() {
  local label="$1"; shift
  local log="${ROOT}/temp/case_${label}.log"
  lsof -ti :$HTTP 2>/dev/null | xargs kill -9 2>/dev/null
  rm -f "${ROOT}/temp/mcert.pem" "${ROOT}/temp/mkey.pem"
  echo "=============== CASE ${label}: env: $* ==============="
  env "$@" \
    YFI_HTTP_ADDR="127.0.0.1:${HTTP}" YFI_LISTEN_ADDR="127.0.0.1:${QUIC}" \
    YFI_REDIS_ADDR="127.0.0.1:1" YFI_POSTGRES_DSN="postgres://x:x@127.0.0.1:1/x?sslmode=disable" \
    YFI_CERT_FILE="${ROOT}/temp/mcert.pem" YFI_KEY_FILE="${ROOT}/temp/mkey.pem" \
    "$BIN" >"$log" 2>&1 &
  local pid=$!
  for i in $(seq 1 15); do
    curl -fsS "http://127.0.0.1:${HTTP}/healthz" >/dev/null 2>&1 && break
    kill -0 $pid 2>/dev/null || break
    sleep 0.4
  done
  if kill -0 $pid 2>/dev/null; then
    echo "--- /ws upgrade probe:"
    curl -s -o /dev/null -w 'HTTP %{http_code}\n' \
      -H "Connection: Upgrade" -H "Upgrade: websocket" \
      -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
      "http://127.0.0.1:${HTTP}/ws"
  else
    echo "--- server EXITED (did not boot)"
  fi
  echo "--- server log:"
  grep -E "mode:|WARNING|NOTICE|WebSocket|refusing" "$log" || cat "$log"
  kill $pid 2>/dev/null
  wait $pid 2>/dev/null
  echo
}

REAL=(ADMIN_SECRET=real-admin-xyz YFI_NONCE_SECRET=real-nonce-xyz YFI_JOIN_SECRET=real-join-xyz)

# (1) both opt-ins, YFI_ENV unset -> /ws upgrades (101)
run_case 1 YFI_DEV_WS=1 YFI_INSECURE_TRANSPORT=1

# (2) YFI_DEV_WS=1 alone -> 404 + NOTICE naming the missing var
run_case 2 YFI_DEV_WS=1

# (3) both opt-ins + YFI_ENV=PROD (real secrets) -> upgrades + loud prod WARNING
run_case 3 YFI_DEV_WS=1 YFI_INSECURE_TRANSPORT=1 YFI_ENV=PROD "${REAL[@]}"

# (4) YFI_ENV=Prod with dev-default secrets -> refuses to boot
run_case 4 YFI_ENV=Prod

# (5) unset YFI_ENV with dev defaults -> still boots as dev (compose/dev-up.sh)
run_case 5 YFI_DEV_WS=1 YFI_INSECURE_TRANSPORT=1 YFI_ENV=

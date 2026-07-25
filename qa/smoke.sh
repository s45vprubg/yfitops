#!/usr/bin/env bash
# qa/smoke.sh — fast reachability check. Boots the gameserver in-memory (no
# Redis/PG needed; it falls back) and confirms /healthz answers, then kills it.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}/server" || exit 1

echo "==> go build"
go build -o /tmp/yfi_gs_smoke ./cmd/gameserver || { echo "SMOKE FAIL: build"; exit 1; }

# Free ports if a prior run left an orphan.
lsof -ti :18777 2>/dev/null | xargs kill -9 2>/dev/null
lsof -ti :14433 2>/dev/null | xargs kill -9 2>/dev/null

echo "==> boot gameserver (in-memory fallback)"
rm -f /tmp/yfi_smoke_cert.pem /tmp/yfi_smoke_key.pem
YFI_HTTP_ADDR=":18777" YFI_LISTEN_ADDR=":14433" \
  YFI_REDIS_ADDR="127.0.0.1:1" YFI_POSTGRES_DSN="postgres://x:x@127.0.0.1:1/x?sslmode=disable" \
  YFI_CERT_FILE="/tmp/yfi_smoke_cert.pem" YFI_KEY_FILE="/tmp/yfi_smoke_key.pem" \
  /tmp/yfi_gs_smoke >/tmp/yfi_smoke.log 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null' EXIT

for i in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:18777/healthz" >/dev/null 2>&1; then
    echo "SMOKE OK: /healthz answered after ${i}s"
    curl -sS "http://127.0.0.1:18777/healthz"; echo
    exit 0
  fi
  sleep 1
done
echo "SMOKE FAIL: /healthz never answered"
tail -30 /tmp/yfi_smoke.log
exit 1

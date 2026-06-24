#!/usr/bin/env bash
# Preflight gate — the "are we actually runnable?" check. Run this BEFORE
# claiming a change is done. It does what a casual `go test` does NOT:
#
#   - Go: build all, vet, test (no infra; integration tests self-skip).
#   - Each frontend: a CLEAN dependency install + a real production BUILD.
#     The production build is the only thing that resolves every import — it is
#     what catches a dependency that's referenced in code but missing from
#     node_modules / package.json (e.g. the @hello-pangea/dnd break). A dev
#     server lazy-resolves per-request and hides this until you hit the page.
#
# Exits non-zero on the first failure with a clear message. CI-friendly.
#
# Usage:
#   scripts/preflight.sh            # full gate
#   scripts/preflight.sh --quick    # skip the clean-reinstall (faster, local)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
QUICK=0
[ "${1:-}" = "--quick" ] && QUICK=1

fail() { echo "❌ PREFLIGHT FAILED: $1" >&2; exit 1; }
ok()   { echo "✅ $1"; }

# ---- Go backend ------------------------------------------------------------
echo "==> [go] build ./..."
( cd "${ROOT}/server" && go build ./... ) || fail "go build"
ok "go build"

echo "==> [go] vet ./..."
( cd "${ROOT}/server" && go vet ./... ) || fail "go vet"
ok "go vet"

echo "==> [go] test ./... (integration tests self-skip without infra)"
( cd "${ROOT}/server" && go test ./... ) || fail "go test"
ok "go tests"

# ---- Frontends -------------------------------------------------------------
for app in stage mobile admin; do
  dir="${ROOT}/web/${app}"
  echo "==> [web/${app}] install"
  if [ "${QUICK}" -eq 1 ]; then
    ( cd "${dir}" && npm install ) >/dev/null 2>&1 || fail "web/${app} npm install"
  else
    # Clean install mirrors a fresh checkout / CI and guarantees node_modules
    # exactly matches package.json — the surest way to surface a missing dep.
    rm -rf "${dir}/node_modules"
    ( cd "${dir}" && npm install ) >/dev/null 2>&1 || fail "web/${app} npm install"
  fi
  ok "web/${app} deps"

  echo "==> [web/${app}] production build (resolves every import)"
  if ! ( cd "${dir}" && npm run build ) >/tmp/yfi_build_${app}.log 2>&1; then
    echo "---- web/${app} build output ----" >&2
    tail -25 "/tmp/yfi_build_${app}.log" >&2
    fail "web/${app} build"
  fi
  ok "web/${app} build"
done

echo ""
echo "🎉 PREFLIGHT PASSED — backend + all three frontends build clean."

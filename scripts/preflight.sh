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
GO_TEST_LOG="$(mktemp)"
( cd "${ROOT}/server" && go test -v ./... ) >"${GO_TEST_LOG}" 2>&1
GO_TEST_STATUS=$?
grep -E '^(--- FAIL|FAIL)' "${GO_TEST_LOG}" >&2
if [ "${GO_TEST_STATUS}" -ne 0 ]; then
  rm -f "${GO_TEST_LOG}"
  fail "go test"
fi
ok "go tests"
# Count DB-gated tests that self-skipped (no YFI_TEST_DSN/PG/REDIS in this
# environment) so the final banner can say plainly what "PREFLIGHT PASSED"
# did NOT cover, instead of letting a green run be read as full coverage.
DB_SKIP_COUNT="$(grep -c -- '--- SKIP' "${GO_TEST_LOG}" 2>/dev/null || true)"
DB_SKIP_COUNT="${DB_SKIP_COUNT:-0}"
rm -f "${GO_TEST_LOG}"

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

if [ "${DB_SKIP_COUNT}" -gt 0 ]; then
  echo ""
  echo "⚠️  ⚠️  ⚠️  WARNING: ${DB_SKIP_COUNT} DB-gated test(s) SELF-SKIPPED — NOT COVERED by this pass:"
  echo "    - store-3 unique-placement guard (placement_staging_test.go)"
  echo "    - RebuildLayout atomic-rollback guard (rebuildlayout_staging_test.go)"
  echo "    - played-state-persistence guard (played_staging_test.go)"
  echo "    - general Redis/Postgres store wiring (test/integration_store_test.go)"
  echo "    PREFLIGHT PASSED means backend+frontends BUILD clean — it does NOT mean"
  echo "    these live-DB guarantees were verified. Run them for real: start"
  echo "    Postgres/Redis (deploy_default network) and run the DB-backed suite in"
  echo "    a golang:1.26 container on that network with YFI_TEST_DSN/YFI_TEST_PG/"
  echo "    YFI_TEST_REDIS set — see qa/HANDOFF.md's staging-verification recipe."
  echo "⚠️  ⚠️  ⚠️"
fi

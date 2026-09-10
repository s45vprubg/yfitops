#!/usr/bin/env bash
# Spin up throwaway Redis + Postgres, apply migrations, run the gated live
# integration tests, then tear everything down. Idempotent; safe to re-run.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REDIS_PORT=16379
PG_PORT=15432

cleanup() {
  docker stop yfi-redis yfi-pg >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> starting redis + postgres"
docker run -d --rm --name yfi-redis -p ${REDIS_PORT}:6379 redis:7-alpine >/dev/null
docker run -d --rm --name yfi-pg -p ${PG_PORT}:5432 \
  -e POSTGRES_USER=yfitops -e POSTGRES_PASSWORD=yfitops -e POSTGRES_DB=yfitops \
  postgres:16-alpine >/dev/null

echo "==> waiting for postgres"
for _ in $(seq 1 30); do
  if docker exec yfi-pg pg_isready -U yfitops >/dev/null 2>&1; then break; fi
  sleep 1
done

# Apply ALL migrations, in lexical order. This used to apply only 0001_init.sql,
# which meant every schema change from 0002 onward was missing and the tests that
# cover them ran against a stale database. Glob rather than list, for the same
# reason deploy/Makefile's migrate target globs: a hardcoded list silently stops
# being complete the moment someone adds a file.
echo "==> applying migrations"
for mig in "${ROOT}/deploy/migrations/"*.sql; do
  echo "    $(basename "${mig}")"
  if ! docker exec -i yfi-pg psql -v ON_ERROR_STOP=1 -U yfitops -d yfitops \
      < "${mig}" >/dev/null; then
    echo "ERROR: migration failed: $(basename "${mig}")" >&2
    exit 1
  fi
done

# Both DSN env var names are exported deliberately. server/test/ gates on
# YFI_TEST_PG; internal/store's staging tests gate on YFI_TEST_DSN. Exporting
# only the first meant the store tests self-skipped here — including the
# regression guard for the store-3 unique-placement fix — while still reporting
# a green run. Set both until the names are unified.
echo "==> running integration + e2e + unit tests"
cd "${ROOT}/server"
PG_DSN="postgres://yfitops:yfitops@localhost:${PG_PORT}/yfitops?sslmode=disable"
YFI_TEST_REDIS=localhost:${REDIS_PORT} \
YFI_TEST_PG="${PG_DSN}" \
YFI_TEST_DSN="${PG_DSN}" \
  go test ./... "$@"

echo "==> done"

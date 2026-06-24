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

echo "==> applying migrations"
docker exec -i yfi-pg psql -U yfitops -d yfitops < "${ROOT}/deploy/migrations/0001_init.sql" >/dev/null

echo "==> running integration + e2e + unit tests"
cd "${ROOT}/server"
YFI_TEST_REDIS=localhost:${REDIS_PORT} \
YFI_TEST_PG="postgres://yfitops:yfitops@localhost:${PG_PORT}/yfitops?sslmode=disable" \
  go test ./... "$@"

echo "==> done"

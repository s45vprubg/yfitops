#!/usr/bin/env bash
# qa/acid.sh — the regression RATCHET. It only ever grows.
#
# Every QA sweep that fixes a bug adds a locking gate here, so a bug killed in
# sweep N cannot quietly come back in sweep N+2 or when a new feature lands.
# Sweeps 1-3 locked their fixes as named Go tests but never listed them in one
# place; sweep 4 codifies that list. A named test disappearing is now a FAILURE,
# not a silent loss of coverage -- that is the whole point of naming them here
# instead of trusting `go test ./...` to still be running them.
#
# Usage: qa/acid.sh            (fast: cold full suite + named-gate presence)
#        RACE=1 qa/acid.sh     (also re-runs the named gates under -race)
#
# DB-backed TestStaging_* gates self-skip without YFI_TEST_DSN; they are checked
# for PRESENCE here and must be run against real Postgres before a release (see
# qa/HANDOFF.md, "STAGING VERIFICATION").
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAILED=0
fail() { echo "ACID FAIL: $*"; FAILED=1; }

# --- Gate 1: the named regression tests must still EXIST -------------------
# Each entry is a bug that a sweep already killed. Sourced from qa/HANDOFF.md.
GATES=(
  # sweep 1
  TestQARegression_StageMessagesRequireStageRole   # engine-1  stage.* authz
  TestQARegression_DailyDoubleCompletesWhenRaterLeaves # engine-3 deadlock
  TestQARegression_BodySizeCapped                  # adminapi-4 body DoS
  # sweep 2
  TestPlay401ForcesRefreshOnLocallyLiveToken       # s2-store-001 forced refresh
  TestValidToken_ConcurrentRefreshSingleFlight     # spotify-1 refresh race
  TestConnDropClosesStreamOnOverflow               # transport-1 zombie conn
  TestConnStopIsIdempotent                         # transport-1 teardown
  # sweep 3
  TestBuzz_EarliestArrivalWinsAndForgedRTTBuysNothing # s3-fe forged RTT
  TestQARegression_SanitizeHandle                  # s3-sanitize ZWJ/bidi
  # crown jewels (pre-existing, must never regress)
  TestBuzz_SingleWinner                            # atomic buzz
  TestBuzz_StaleNonceDropped                       # 4D nonce
  # post-sweep-3 / staging (DB-gated, presence-checked)
  TestStaging_PlaceTrack_MovesInsteadOfDuplicating  # store-3 unique placement
  TestStaging_PlayedStatePersistsAcrossReload       # s2-store-002
  TestStaging_RebuildLayout_AtomicRollback          # adminapi-2/ai-5 atomicity
  # session limiter (post-sweep-2)
  TestE2E_PerIPSessionCapRejectsExcess              # flood cap
  # sweep 4 — the /ws second door: every transport guarantee re-established
  TestWSHandlerHonorsSessionLimiter                 # s4-ws-c/x: limiter bypass
  TestWSHandlerDropTearsDownSocket                  # s4-ws: zombie conn (no closer)
  TestWSHandlerIdleTimeout                          # s4-ws: no idle reaper
  TestWSHandlerFramingRoundTrip                     # s4-ws: first /ws coverage at all
  # sweep 4 — projected points must equal awarded points
  TestPool_ProjectedMatchesAwarded                  # s4-engine: stage showed 140, paid 70
  TestPool_AdminReadoutMatchesAward                 # s4-engine: admin showed 190
  TestPool_StageReconnectKeepsReducedPool           # s4-engine: reconnect restored the row pool
  TestPool_LivePoolNeverInverts                     # s4-engine: base>max -> upward decay
  TestCurrentPointsFromPool_MatchesCurrentPoints    # s4-engine: the two curves must agree
  # sweep 4 — buzz fairness + the prod-gate matrix
  TestQARegression_BuzzTieBreakIsRandomNotPlayerID  # s4-x1: low playerID won every tie
  TestQARegression_BuzzEarliestArrivalStillWinsAfterShuffle # s4-x1: shuffle must not break §4B
  TestDecideWS                                      # s4-x2: /ws opt-in matrix
)
echo "==> gate presence (${#GATES[@]} locked regressions)"
for g in "${GATES[@]}"; do
  grep -rqE "func ${g}\(" --include='*_test.go' "${ROOT}/server" \
    || fail "locked regression test ${g} no longer exists"
done
[[ ${FAILED} -eq 0 ]] && echo "    all ${#GATES[@]} present"

# --- Gate 2: cold full Go suite (never a cached green) ---------------------
echo "==> go build / vet"
(cd "${ROOT}/server" && go build ./... ) || fail "go build"
(cd "${ROOT}/server" && go vet ./... )  || fail "go vet"

echo "==> go test -count=1 ./... (cold)"
(cd "${ROOT}/server" && go test -count=1 ./... ) || fail "go test"

if [[ "${RACE:-0}" == "1" ]]; then
  echo "==> go test -count=1 -race ./... "
  (cd "${ROOT}/server" && go test -count=1 -race ./... ) || fail "go test -race"
fi

# --- Gate 3: the named gates actually RUN (not just exist, not skipped) ----
# A gate that only exists but is skipped locks in nothing. TestStaging_* and the
# UDP-binding TestE2E_* are excluded: they legitimately skip without YFI_TEST_DSN
# / a UDP-capable sandbox.
echo "==> named gates execute"
RUNNABLE=$(printf '%s\n' "${GATES[@]}" | grep -v '^TestStaging_' | grep -v '^TestE2E_' | paste -sd'|' -)
OUT=$( cd "${ROOT}/server" && go test -count=1 -run "^(${RUNNABLE})$" ./... 2>&1 )
echo "${OUT}" | grep -q "FAIL" && { echo "${OUT}"; fail "a named gate failed"; }
# Every runnable gate must report as run somewhere, not silently filtered away.
NRUN=$( cd "${ROOT}/server" && go test -count=1 -v -run "^(${RUNNABLE})$" ./... 2>&1 | grep -cE '^(=== RUN|    --- RUN)' )
EXPECT=$(printf '%s\n' "${GATES[@]}" | grep -v '^TestStaging_' | grep -v '^TestE2E_' | wc -l | tr -d ' ')
[[ "${NRUN}" -lt "${EXPECT}" ]] && fail "only ${NRUN} of ${EXPECT} runnable gates executed"

# --- Gate 4: smoke (cold boot, /healthz) ----------------------------------
echo "==> smoke"
"${ROOT}/qa/smoke.sh" >/dev/null 2>&1 || fail "smoke.sh"

echo
if [[ ${FAILED} -eq 0 ]]; then echo "ACID PASSED"; exit 0; fi
echo "ACID FAILED"; exit 1

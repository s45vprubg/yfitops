// scoring.ts — mirror of server/internal/game/scoring.go (design_doc §7).
// FIXED CONTRACT. The stage's 60fps point timer uses currentPoints() and must
// match the Go backend at the floor (§5). Keep constants identical.

export const BASE_VALUE = 100;
export const DECAY_HOLD_SECONDS = 5.0;
export const DECAY_END_SECONDS = 60.0;
export const PARTIAL_POINTS = 50;

export function rowMultiplier(row: number): number {
  if (row <= 1) return 1.0;
  if (row === 2) return 1.25;
  if (row === 3) return 1.5;
  if (row === 4) return 1.75;
  return 2.0;
}

export function maxPointsForRow(row: number): number {
  return Math.floor(BASE_VALUE * rowMultiplier(row));
}

export function bonusCeiling(row: number): number {
  return maxPointsForRow(row) - BASE_VALUE;
}

// currentPoints mirrors game.CurrentPoints — floored, linear decay 5s..60s.
export function currentPoints(row: number, elapsedMs: number): number {
  const t = elapsedMs / 1000.0;
  const bonus = bonusCeiling(row);
  if (t <= DECAY_HOLD_SECONDS) return maxPointsForRow(row);
  if (t >= DECAY_END_SECONDS) return BASE_VALUE;
  const span = DECAY_END_SECONDS - DECAY_HOLD_SECONDS; // 55s
  const frac = 1.0 - (t - DECAY_HOLD_SECONDS) / span;
  return Math.floor(BASE_VALUE + bonus * frac);
}

// currentPointsFromPool — same linear decay but uses explicit ceiling/floor
// instead of computing from row. Used after a partial grade shifts the pool.
export function currentPointsFromPool(max: number, base: number, elapsedMs: number): number {
  const t = elapsedMs / 1000.0;
  const bonus = max - base;
  if (t <= DECAY_HOLD_SECONDS) return max;
  if (t >= DECAY_END_SECONDS) return base;
  const span = DECAY_END_SECONDS - DECAY_HOLD_SECONDS;
  const frac = 1.0 - (t - DECAY_HOLD_SECONDS) / span;
  return Math.floor(base + bonus * frac);
}

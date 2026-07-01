// decrypt.ts — three-phase text reveal animation for the stage display.
//
// Phase 1: Fixed-width block of cycling random characters (NOISE_WIDTH per row).
//          Duration: PHASE1_MS.
//
// Phase 2: Switches to exact answer length — cycling characters with spaces
//          preserved (word boundaries visible). Duration: instant transition,
//          then holds until phase 3 starts.
//
// Phase 3: One random non-space character revealed every REVEAL_INTERVAL_MS.
//          Spaces are visible from the start of phase 2. Unrevealed slots
//          keep cycling.

const GLYPHS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";

// --- Tunable timing (ms) ---
export const PHASE1_MS = 5000;
export const REVEAL_INTERVAL_MS = 2000;

// Fixed character count for the noise block in phase 1.
const NOISE_WIDTH = 20;

function hash(n: number): number {
  let x = n | 0;
  x = (x ^ 61) ^ (x >>> 16);
  x = x + (x << 3);
  x = x ^ (x >>> 4);
  x = Math.imul(x, 0x27d4eb2d);
  x = x ^ (x >>> 15);
  return (x >>> 0) / 0xffffffff;
}

// glyphAt returns a deterministic pseudo-random glyph for a (slot, tick) pair.
// Exported so the stage can render cosmetic noise for not-yet-revealed slots in
// the server-driven decrypt (see ActiveRound.tsx). Carries no answer info.
export function glyphAt(slot: number, tick: number): string {
  const r = hash(slot * 92821 + tick * 2654435761);
  return GLYPHS[Math.floor(r * GLYPHS.length)];
}

export type Phase = 1 | 2 | 3 | 4;

export interface DecryptFrame {
  text: string;
  phase: Phase;
  revealProgress: number;
}

export interface DecryptInput {
  elapsedMs: number;
  targetLen: number;
  target?: string;
  seed: number;
}

// Build reveal order for phase 3: non-space character indices in random order.
function buildRevealOrder(target: string, seed: number): number[] {
  const indices: number[] = [];
  for (let i = 0; i < target.length; i++) {
    if (target[i] !== " ") indices.push(i);
  }
  for (let i = indices.length - 1; i > 0; i--) {
    const j = Math.floor(hash(seed * 7919 + i) * (i + 1));
    const tmp = indices[i];
    indices[i] = indices[j];
    indices[j] = tmp;
  }
  return indices;
}

export function computeFrame(input: DecryptInput): DecryptFrame {
  const { elapsedMs, targetLen, target, seed } = input;
  const tick = Math.floor(elapsedMs / 200); // ~5fps cycling

  // ---- Phase 1: fixed-width noise block ----
  if (elapsedMs < PHASE1_MS) {
    let s = "";
    for (let i = 0; i < NOISE_WIDTH; i++) s += glyphAt(seed + i, tick);
    return { text: s, phase: 1, revealProgress: 0 };
  }

  // ---- Phase 2/3: answer-length string with spaces shown ----
  // Without the target text, show cycling characters at exact length.
  if (!target) {
    let s = "";
    for (let i = 0; i < targetLen; i++) s += glyphAt(seed + i, tick);
    return { text: s, phase: 2, revealProgress: 0 };
  }

  // Phase 3: reveal one character every REVEAL_INTERVAL_MS.
  const phase3Elapsed = elapsedMs - PHASE1_MS;
  const revealOrder = buildRevealOrder(target, seed);
  const totalToReveal = revealOrder.length;
  const revealedCount = Math.min(totalToReveal, Math.floor(phase3Elapsed / REVEAL_INTERVAL_MS));

  const revealedSet = new Set<number>();
  for (let i = 0; i < revealedCount; i++) revealedSet.add(revealOrder[i]);

  let s = "";
  for (let i = 0; i < target.length; i++) {
    if (target[i] === " ") {
      s += " ";
    } else if (revealedSet.has(i)) {
      s += target[i];
    } else {
      s += glyphAt(seed + i, tick);
    }
  }

  const done = revealedCount >= totalToReveal;
  return {
    text: s,
    phase: done ? 4 : 3,
    revealProgress: totalToReveal > 0 ? revealedCount / totalToReveal : 1,
  };
}

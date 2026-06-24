// decrypt.ts — the deterministic, three-phase cryptographic decryption routine
// (design_doc §5). PURE LOGIC: given the elapsed time since trackStart and the
// known string lengths (+ the true string once the reveal arrives), it computes
// the exact frame of text to display. The React layer drives it from a single
// requestAnimationFrame loop (NOT setInterval) so there's no stutter (§5).
//
// Why decoupled from React: keeping the frame computation pure makes it
// deterministic and trivially testable, and lets one rAF loop render both the
// artist and song lines in lockstep.
//
// Phases:
//   Phase 1  [0, GLYPH_PHASE_MS)        rapid randomized glyph cycling. Noise
//                                        LENGTH is decoupled from the target
//                                        (intentionally wrong length).
//   Phase 2  [GLYPH, MASK_PHASE_MS)     masked underscores preserving the EXACT
//                                        target length (uses artistLen/songLen).
//   Phase 3  [MASK_PHASE_MS, +reveal)   Wheel-of-Fortune progressive reveal of
//                                        the true characters. Only runs once the
//                                        reveal payload (the real string) exists.
//
// Before the reveal payload arrives we can still render phases 1 & 2 from
// lengths alone. Phase 3 needs the real characters; until then we hold phase 2.

const GLYPHS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()_+-=<>?/\\|{}[]~";

// Timeline (ms from trackStart). Tunable; chosen to read well on a projector.
export const GLYPH_PHASE_MS = 1400; // phase 1 duration
export const MASK_PHASE_MS = 2600; // phase 1 + phase 2 end (phase 2 lasts 1200ms)
export const REVEAL_INTERVAL_MS = 130; // ms between each newly-revealed character

// Deterministic pseudo-random so the same (seed, frame) yields the same glyph —
// keeps the animation reproducible and flicker-stable per character slot.
function hash(n: number): number {
  let x = n | 0;
  x = (x ^ 61) ^ (x >>> 16);
  x = x + (x << 3);
  x = x ^ (x >>> 4);
  x = Math.imul(x, 0x27d4eb2d);
  x = x ^ (x >>> 15);
  return (x >>> 0) / 0xffffffff;
}

function glyphAt(slot: number, tick: number): string {
  const r = hash(slot * 92821 + tick * 2654435761);
  return GLYPHS[Math.floor(r * GLYPHS.length)];
}

export type Phase = 1 | 2 | 3 | 4; // 4 = fully revealed / settled

export interface DecryptFrame {
  text: string;
  phase: Phase;
  // 0..1 progress through phase 3, for optional glow effects.
  revealProgress: number;
}

export interface DecryptInput {
  elapsedMs: number;
  targetLen: number; // exact length to preserve in phases 2/3 (from trackStart)
  // The true string. Undefined until the reveal payload arrives.
  target?: string;
  // Per-line seed so artist and song don't cycle identically.
  seed: number;
}

// Noise length for phase 1 — intentionally decoupled from the target (§5).
function noiseLen(targetLen: number, seed: number): number {
  const wobble = Math.floor(hash(seed) * 6) - 2; // -2..+3
  return Math.max(6, targetLen + wobble + 3);
}

export function computeFrame(input: DecryptInput): DecryptFrame {
  const { elapsedMs, targetLen, target, seed } = input;

  // ---- Phase 1: rapid randomized glyph cycling, decoupled length ----
  if (elapsedMs < GLYPH_PHASE_MS) {
    const len = noiseLen(targetLen, seed);
    const tick = Math.floor(elapsedMs / 45); // ~22 cycles/sec
    let s = "";
    for (let i = 0; i < len; i++) s += glyphAt(seed + i, tick);
    return { text: s, phase: 1, revealProgress: 0 };
  }

  // ---- Phase 2: masked underscores, exact target length ----
  if (elapsedMs < MASK_PHASE_MS || !target) {
    // Underscore for non-space slots; preserve spaces so word geometry shows.
    let s = "";
    for (let i = 0; i < targetLen; i++) {
      if (target && target[i] === " ") s += " ";
      else s += "_";
    }
    return { text: s, phase: 2, revealProgress: 0 };
  }

  // ---- Phase 3: Wheel-of-Fortune progressive reveal ----
  const sinceMask = elapsedMs - MASK_PHASE_MS;
  const realLen = target.length;
  // How many true characters have been revealed so far.
  const revealedCount = Math.min(realLen, Math.floor(sinceMask / REVEAL_INTERVAL_MS) + 1);

  // Deterministic reveal ORDER: shuffle slot indices by hash(seed,i) so letters
  // pop in pseudo-random positions ("randomized true character placement", §5).
  const order = buildRevealOrder(realLen, seed);
  const revealedSet = new Set<number>();
  for (let i = 0; i < revealedCount; i++) revealedSet.add(order[i]);

  const settleTick = Math.floor(elapsedMs / 45);
  let s = "";
  for (let i = 0; i < realLen; i++) {
    if (target[i] === " ") s += " ";
    else if (revealedSet.has(i)) s += target[i];
    else s += glyphAt(seed + i + 7, settleTick); // unrevealed slots keep cycling
  }

  const done = revealedCount >= countNonSpace(target, order, realLen);
  return {
    text: s,
    phase: done ? 4 : 3,
    revealProgress: realLen > 0 ? revealedCount / realLen : 1,
  };
}

// Reveal order excludes spaces (they're shown immediately) and is a stable
// hash-shuffle of the remaining indices.
function buildRevealOrder(len: number, seed: number): number[] {
  const idx: number[] = [];
  for (let i = 0; i < len; i++) idx.push(i);
  // Fisher-Yates with deterministic hash-driven swaps.
  for (let i = len - 1; i > 0; i--) {
    const j = Math.floor(hash(seed * 7919 + i) * (i + 1));
    const tmp = idx[i];
    idx[i] = idx[j];
    idx[j] = tmp;
  }
  return idx;
}

function countNonSpace(target: string, _order: number[], len: number): number {
  // Reveal counter counts every slot (spaces included in the index math), so
  // "done" is reached when revealedCount has covered the full length.
  void target;
  return len;
}

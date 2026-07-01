// ActiveRound — the showpiece. Two massive centered lines (Artist + Song)
// running the server-authoritative decrypt reveal (§4A/§5), plus the prominent
// real-time point timer.
//
// The reveal is NO LONGER computed locally. The server owns the reveal clock
// and streams a MASKED frame (maskedReveal) to both the stage and the phones in
// the same broadcast, so the projector and every phone show the identical
// letters at the identical moment and a phone can never read ahead. This
// component just renders the current mask:
//   - a revealed ("locked") letter -> shown in the LOCKED color
//   - a space -> shown as a space
//   - a not-yet-revealed slot -> cosmetic local noise (glyphAt) in the CYCLING
//     color. The noise carries no information about the answer.
//
// ONE requestAnimationFrame loop drives the noise cycling + the point timer.
// The point timer stays local and deterministic (scoring.currentPointsFromPool,
// which honors the reduced pool after a partial grade), freezing on buzz to
// mask latency (§5).

import { useEffect, useRef } from "react";
import type { MaskedRevealData, TrackStartData } from "@shared/protocol";
import { currentPointsFromPool } from "@shared/scoring";
import { glyphAt } from "../anim/decrypt";
import type { TimerAnchor } from "../net/useGame";

interface Props {
  trackStart: TrackStartData;
  timer: TimerAnchor;
  maskedReveal: MaskedRevealData | null;
  lockoutHandle: string | null;
}

// Seeds keep the two lines' noise cycling independently.
const ARTIST_SEED = 1337;
const SONG_SEED = 8675309;

// Fixed noise width shown before the server sends a length skeleton (phase 1).
const NOISE_WIDTH = 20;

// Tailwind classes for the letter states.
const LOCKED_CLS = "text-neon-green neon-text";       // a real letter is revealed
const NOISE_CLS = "text-neon-cyan/90 neon-cyan";       // cosmetic cycling noise
const LENGTH_CLS = "text-neon-amber neon-text";        // "correct length now" flash on block collapse

// How long the "length confirmed" amber flash lasts after the block collapses
// to the real length (phase 1 -> 2).
const LENGTH_FLASH_MS = 1200;

export default function ActiveRound({ trackStart, timer, maskedReveal, lockoutHandle }: Props) {
  const artistRef = useRef<HTMLDivElement>(null);
  const songRef = useRef<HTMLDivElement>(null);
  const pointsRef = useRef<HTMLDivElement>(null);

  const stateRef = useRef({ trackStart, timer, maskedReveal });
  stateRef.current = { trackStart, timer, maskedReveal };

  useEffect(() => {
    let raf = 0;
    let lastPoints = -1;
    let tick = 0;
    let prevPhase = 0;
    let lengthFlashUntil = 0; // performance.now() deadline for the amber flash
    let morphStart = -1;      // performance.now() when the char-drop morph began
    let blockWidth = 20;      // slot count we were showing during the block phase
    let prevBlockLen = 20;    // last-seen phase-1 block width (from mask lengths)

    // Render one field's row of per-character spans into `el`. Revealed slots
    // use the mask char + locked color; hidden slots cycle noise. When the
    // real length was just confirmed (block collapse) hidden slots briefly
    // flash the LENGTH color so the room sees "this is the right length now".
    // renderLen lets the caller override the slot count (for the char-drop
    // morph); when undefined the mask/real length is used and mask chars render.
    const renderField = (
      el: HTMLDivElement | null,
      mask: string[] | undefined,
      fallbackLen: number,
      seed: number,
      t: number,
      lengthFlash: boolean,
      renderLen?: number,
    ) => {
      if (!el) return;
      const realLen = mask ? mask.length : fallbackLen > 0 ? fallbackLen : NOISE_WIDTH;
      const len = renderLen ?? realLen;
      const morphing = renderLen !== undefined; // during the drop, ignore mask chars (all noise)
      const hiddenCls = lengthFlash ? LENGTH_CLS : NOISE_CLS;

      if (el.childElementCount !== len) {
        el.textContent = "";
        for (let i = 0; i < len; i++) {
          el.appendChild(document.createElement("span"));
        }
      }

      for (let i = 0; i < len; i++) {
        const span = el.children[i] as HTMLSpanElement;
        const cell = !morphing && mask ? mask[i] : "";
        if (cell === " ") {
          if (span.textContent !== " ") span.textContent = " ";
          if (span.className !== hiddenCls) span.className = hiddenCls;
        } else if (cell) {
          if (span.textContent !== cell) span.textContent = cell;
          if (span.className !== LOCKED_CLS) span.className = LOCKED_CLS;
        } else {
          span.textContent = glyphAt(seed + i, t);
          if (span.className !== hiddenCls) span.className = hiddenCls;
        }
      }
    };

    // morphLen interpolates the rendered slot count from the phase-1 block width
    // down to the field's real length over easeMs, so the block visibly "drops"
    // characters until it hits the correct length (then the flash fires).
    const morphLen = (fromLen: number, toLen: number, elapsed: number, easeMs: number): number | undefined => {
      if (easeMs <= 0 || elapsed >= easeMs || fromLen <= toLen) return undefined; // done: real length
      const p = elapsed / easeMs;
      return Math.max(toLen, Math.round(fromLen - (fromLen - toLen) * p));
    };

    // fitToWidth horizontally scales a line down so it never overflows its
    // container (long titles). Uses the unscaled scrollWidth vs the parent's
    // width; scaleX only downward. Cheap enough to run each frame.
    const fitToWidth = (el: HTMLDivElement | null) => {
      if (!el || !el.parentElement) return;
      const avail = el.parentElement.clientWidth;
      if (avail <= 0) return;
      // Measure natural width by temporarily clearing the transform.
      el.style.transform = "";
      const natural = el.scrollWidth;
      const scale = natural > avail ? avail / natural : 1;
      el.style.transform = scale < 1 ? `scaleX(${scale})` : "";
      el.style.transformOrigin = "center";
    };

    const loop = () => {
      const { trackStart: ts, timer: tm, maskedReveal: mr } = stateRef.current;
      const now = Date.now();

      // ~5fps noise cycling, frozen while the timer is frozen (buzz).
      if (!tm.frozen) tick = Math.floor(now / 200);

      // Detect the block -> skeleton collapse (phase 1 -> 2): start the char-drop
      // morph, and after it lands flash the "correct length" color.
      const phase = mr?.phase ?? 0;
      if (prevPhase === 1 && phase >= 2) {
        morphStart = performance.now();
        blockWidth = prevBlockLen; // the phase-1 block width we were showing
      }
      prevPhase = phase;
      prevBlockLen = mr?.artistLen ?? prevBlockLen;

      const easeMs = mr?.easeMs ?? 0;
      const nowP = performance.now();
      const morphElapsed = morphStart >= 0 ? nowP - morphStart : Infinity;
      const morphing = morphStart >= 0 && morphElapsed < easeMs;
      // Flash starts the instant the morph completes.
      if (morphStart >= 0 && !morphing && lengthFlashUntil === 0) {
        lengthFlashUntil = nowP + LENGTH_FLASH_MS;
      }
      const lengthFlash = lengthFlashUntil > 0 && nowP < lengthFlashUntil;

      const aReal = mr?.artist?.length ?? mr?.artistLen ?? ts.artistLen;
      const sReal = mr?.song?.length ?? mr?.songLen ?? ts.songLen;
      const aLen = morphing ? morphLen(blockWidth, aReal, morphElapsed, easeMs) : undefined;
      const sLen = morphing ? morphLen(blockWidth, sReal, morphElapsed, easeMs) : undefined;

      renderField(artistRef.current, mr?.artist, mr?.artistLen ?? ts.artistLen, ARTIST_SEED, tick, lengthFlash, aLen);
      renderField(songRef.current, mr?.song, mr?.songLen ?? ts.songLen, SONG_SEED, tick, lengthFlash, sLen);

      // Shrink a line that's wider than its container so long artist/song text
      // always fits on screen (scaleX down; never scale up past 1).
      fitToWidth(artistRef.current);
      fitToWidth(songRef.current);

      // --- point timer (local, deterministic; honors reduced pool post-partial) ---
      if (pointsRef.current && !tm.frozen) {
        const pts = currentPointsFromPool(tm.maxPoints, tm.basePoints, Math.max(0, now - tm.startTime));
        if (pts !== lastPoints) {
          lastPoints = pts;
          pointsRef.current.textContent = String(pts);
        }
      }

      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, []);

  const frozen = timer.frozen;
  // Glow the lines once the reveal is streaming (phase >= 3).
  const glow = (maskedReveal?.phase ?? 0) >= 3;

  return (
    <div className="flex h-full w-full flex-col items-center justify-center px-12">
      {/* Point timer */}
      <div className="mb-12 flex flex-col items-center">
        <div className="mb-1 text-sm uppercase tracking-[0.5em] text-neon-cyan/60">
          {frozen ? "locked" : "points available"}
        </div>
        <div
          ref={pointsRef}
          className={[
            "tnum text-[10rem] font-extrabold leading-none transition-opacity duration-150",
            frozen ? "text-neon-magenta opacity-30 blur-[1px]" : "text-neon-green neon-text animate-pulseGlow",
          ].join(" ")}
        >
          {currentPointsFromPool(timer.maxPoints, timer.basePoints, Math.max(0, Date.now() - timer.startTime))}
        </div>
      </div>

      {/* Decryption lines */}
      <div className="flex w-full max-w-[90vw] flex-col items-center gap-8">
        <Line label="artist" textRef={artistRef} glow={glow} />
        <Line label="song" textRef={songRef} glow={glow} />
      </div>

      {lockoutHandle && (
        <div className="mt-12 text-3xl font-bold text-neon-magenta neon-cyan animate-winnerPop">
          🔒 {lockoutHandle} is guessing…
        </div>
      )}
    </div>
  );
}

function Line({
  label,
  textRef,
  glow,
}: {
  label: string;
  textRef: React.RefObject<HTMLDivElement | null>;
  glow: boolean;
}) {
  return (
    <div className="flex w-full flex-col items-center">
      <span className="mb-1 text-xs uppercase tracking-[0.6em] text-neon-cyan/40">{label}</span>
      <div
        ref={textRef}
        className={[
          "tnum whitespace-pre text-center font-mono font-bold tracking-[0.15em]",
          "text-[clamp(2rem,6vw,5.5rem)] leading-tight",
          glow ? "opacity-100" : "opacity-95",
        ].join(" ")}
      />
    </div>
  );
}

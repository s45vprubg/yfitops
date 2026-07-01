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

// Tailwind classes for the two letter states.
const LOCKED_CLS = "text-neon-green neon-text";
const NOISE_CLS = "text-neon-cyan/90 neon-cyan";

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

    // Render one field's row of per-character spans into `el`. Revealed slots
    // use the mask char + locked color; hidden slots cycle noise. Reuses spans
    // across frames to avoid thrashing the DOM.
    const renderField = (
      el: HTMLDivElement | null,
      mask: string[] | undefined,
      fallbackLen: number,
      seed: number,
      t: number,
    ) => {
      if (!el) return;
      // Determine the slot count: mask length once known, else the noise block.
      const len = mask ? mask.length : fallbackLen > 0 ? fallbackLen : NOISE_WIDTH;

      // (Re)build the span row if the length changed.
      if (el.childElementCount !== len) {
        el.textContent = "";
        for (let i = 0; i < len; i++) {
          el.appendChild(document.createElement("span"));
        }
      }

      for (let i = 0; i < len; i++) {
        const span = el.children[i] as HTMLSpanElement;
        const cell = mask ? mask[i] : "";
        if (cell === " ") {
          if (span.textContent !== " ") span.textContent = " ";
          if (span.className !== NOISE_CLS) span.className = NOISE_CLS;
        } else if (cell) {
          // Revealed/locked letter.
          if (span.textContent !== cell) span.textContent = cell;
          if (span.className !== LOCKED_CLS) span.className = LOCKED_CLS;
        } else {
          // Hidden slot -> cosmetic noise.
          span.textContent = glyphAt(seed + i, t);
          if (span.className !== NOISE_CLS) span.className = NOISE_CLS;
        }
      }
    };

    const loop = () => {
      const { trackStart: ts, timer: tm, maskedReveal: mr } = stateRef.current;
      const now = Date.now();

      // ~5fps noise cycling, frozen while the timer is frozen (buzz).
      if (!tm.frozen) tick = Math.floor(now / 200);

      renderField(artistRef.current, mr?.artist, mr?.artistLen ?? ts.artistLen, ARTIST_SEED, tick);
      renderField(songRef.current, mr?.song, mr?.songLen ?? ts.songLen, SONG_SEED, tick);

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

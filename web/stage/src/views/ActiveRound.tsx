// ActiveRound — the showpiece. Two massive centered lines (Artist + Song)
// running the staggered cryptographic decryption animation (§5), plus the
// prominent real-time point timer.
//
// ONE requestAnimationFrame loop drives BOTH the decryption text and the timer
// (NOT setInterval — §5 demands rAF for stutter-free rendering). Each frame:
//   1. elapsed = serverNow() - trackStart.startTime
//   2. decryption: computeFrame() for the artist + song lines from lengths
//      (phases 1-2) and the revealed strings (phase 3, once reveal arrives)
//   3. timer: scoring.currentPoints(row, elapsed) — floored, identical to backend
//
// On a buzz the timer FREEZES instantly (timer.frozen, set when state ->
// LOCKED_OUT in useGame) to mask any latency discrepancy with the server's
// authoritative score (§5). A new trackStart (post-partial recalibration)
// re-anchors elapsed and un-freezes.

import { useEffect, useRef, useState } from "react";
import type { RevealData, TrackStartData } from "@shared/protocol";
import { currentPoints } from "@shared/scoring";
import { computeFrame } from "../anim/decrypt";
import type { TimerAnchor } from "../net/useGame";

interface Props {
  trackStart: TrackStartData;
  timer: TimerAnchor;
  reveal: RevealData | null;
  revealedArtist: boolean;
  revealedSong: boolean;
  lockoutHandle: string | null;
}

// Seeds keep the two lines cycling independently.
const ARTIST_SEED = 1337;
const SONG_SEED = 8675309;

export default function ActiveRound({ trackStart, timer, reveal, revealedArtist, revealedSong, lockoutHandle }: Props) {
  const artistRef = useRef<HTMLDivElement>(null);
  const songRef = useRef<HTMLDivElement>(null);
  const pointsRef = useRef<HTMLDivElement>(null);
  const [phaseGlow, setPhaseGlow] = useState(false);

  const stateRef = useRef({ trackStart, timer, reveal, revealedArtist, revealedSong, lockoutHandle });
  stateRef.current = { trackStart, timer, reveal, revealedArtist, revealedSong, lockoutHandle };

  useEffect(() => {
    let raf = 0;
    let lastPoints = -1;
    let lastGlow = false;
    let frozenElapsed = -1;

    const tick = () => {
      const { trackStart: ts, timer: tm, reveal: rv, revealedArtist: ra, revealedSong: rs } = stateRef.current;
      const now = Date.now();
      const elapsed = Math.max(0, now - ts.startTime);

      // Freeze the animation clock when the timer is frozen (buzz in progress).
      let animElapsed: number;
      if (tm.frozen) {
        if (frozenElapsed < 0) frozenElapsed = elapsed;
        animElapsed = frozenElapsed;
      } else {
        frozenElapsed = -1;
        animElapsed = elapsed;
      }

      // --- decryption text ---
      // If a field was force-revealed (partial grade), show it directly.
      let artistPhase = 4;
      let songPhase = 4;
      if (ra && rv?.artist) {
        if (artistRef.current) artistRef.current.textContent = rv.artist;
      } else {
        const af = computeFrame({ elapsedMs: animElapsed, targetLen: ts.artistLen, target: rv?.artist, seed: ARTIST_SEED });
        if (artistRef.current) artistRef.current.textContent = af.text;
        artistPhase = af.phase;
      }
      if (rs && rv?.song) {
        if (songRef.current) songRef.current.textContent = rv.song;
      } else {
        const sf = computeFrame({ elapsedMs: animElapsed, targetLen: ts.songLen, target: rv?.song, seed: SONG_SEED });
        if (songRef.current) songRef.current.textContent = sf.text;
        songPhase = sf.phase;
      }

      const inReveal = artistPhase >= 3 || songPhase >= 3;
      if (inReveal !== lastGlow) {
        lastGlow = inReveal;
        setPhaseGlow(inReveal);
      }

      // --- point timer ---
      if (pointsRef.current) {
        if (tm.frozen) {
          // Freeze: stop updating the displayed number. Visual handled by class.
        } else {
          const pts = currentPoints(tm.row, elapsed);
          if (pts !== lastPoints) {
            lastPoints = pts;
            pointsRef.current.textContent = String(pts);
          }
        }
      }

      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, []);

  const frozen = timer.frozen;

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
          {currentPoints(timer.row, Math.max(0, Date.now() - trackStart.startTime))}
        </div>
      </div>

      {/* Decryption lines */}
      <div className="flex w-full max-w-[90vw] flex-col items-center gap-8">
        <Line label="artist" textRef={artistRef} glow={phaseGlow} />
        <Line label="song" textRef={songRef} glow={phaseGlow} />
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
          glow ? "text-neon-green neon-text" : "text-neon-cyan/90 neon-cyan",
        ].join(" ")}
      />
    </div>
  );
}

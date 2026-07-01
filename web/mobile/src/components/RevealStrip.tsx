// RevealStrip — mobile rendering of the server-authoritative letter reveal.
//
// Shows the SAME decrypt frame as the projector (§4A extension): the server
// streams a masked frame to both surfaces in one broadcast, so this can never
// show a letter the projector hasn't. Revealed ("locked") letters render in the
// accent color; not-yet-revealed slots cycle cosmetic noise (no answer info);
// spaces are shown. Word lengths are visible by design (same as the stage).

import { useEffect, useRef } from "react";
import type { MaskedRevealData } from "@shared/protocol";

const GLYPHS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
const ARTIST_SEED = 1337;
const SONG_SEED = 8675309;
const NOISE_WIDTH = 14; // phase-1 block before lengths are known (mobile is narrower)

// Same deterministic hash/glyph as the stage so noise looks consistent.
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

const LOCKED_CLS = "text-guess";
const NOISE_CLS = "text-neutral-500";
const LENGTH_CLS = "text-amber-400"; // "correct length now" flash on block collapse
const LENGTH_FLASH_MS = 1200;

export function RevealStrip({ mask }: { mask: MaskedRevealData | null }) {
  const artistRef = useRef<HTMLDivElement>(null);
  const songRef = useRef<HTMLDivElement>(null);
  const maskRef = useRef(mask);
  maskRef.current = mask;

  useEffect(() => {
    let raf = 0;
    let prevPhase = 0;
    let lengthFlashUntil = 0;
    const renderField = (
      el: HTMLDivElement | null,
      field: string[] | undefined,
      fallbackLen: number,
      seed: number,
      tick: number,
      lengthFlash: boolean,
    ) => {
      if (!el) return;
      const len = field ? field.length : fallbackLen > 0 ? fallbackLen : NOISE_WIDTH;
      const hiddenCls = lengthFlash ? LENGTH_CLS : NOISE_CLS;
      if (el.childElementCount !== len) {
        el.textContent = "";
        for (let i = 0; i < len; i++) el.appendChild(document.createElement("span"));
      }
      for (let i = 0; i < len; i++) {
        const span = el.children[i] as HTMLSpanElement;
        const cell = field ? field[i] : "";
        if (cell === " ") {
          if (span.textContent !== " ") span.textContent = " ";
          if (span.className !== hiddenCls) span.className = hiddenCls;
        } else if (cell) {
          if (span.textContent !== cell) span.textContent = cell;
          if (span.className !== LOCKED_CLS) span.className = LOCKED_CLS;
        } else {
          span.textContent = glyphAt(seed + i, tick);
          if (span.className !== hiddenCls) span.className = hiddenCls;
        }
      }
    };

    const loop = () => {
      const m = maskRef.current;
      const tick = Math.floor(Date.now() / 200); // ~5fps
      const phase = m?.phase ?? 0;
      if (prevPhase === 1 && phase >= 2) {
        lengthFlashUntil = performance.now() + LENGTH_FLASH_MS;
      }
      prevPhase = phase;
      const lengthFlash = performance.now() < lengthFlashUntil;
      renderField(artistRef.current, m?.artist, m?.artistLen ?? 0, ARTIST_SEED, tick, lengthFlash);
      renderField(songRef.current, m?.song, m?.songLen ?? 0, SONG_SEED, tick, lengthFlash);
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, []);

  return (
    <div className="flex w-full max-w-sm flex-col items-center gap-4">
      <Field label="artist" textRef={artistRef} />
      <Field label="song" textRef={songRef} />
    </div>
  );
}

function Field({ label, textRef }: { label: string; textRef: React.RefObject<HTMLDivElement | null> }) {
  return (
    <div className="flex w-full flex-col items-center">
      <span className="mb-1 text-[0.6rem] uppercase tracking-[0.4em] text-neutral-600">{label}</span>
      <div
        ref={textRef}
        className="whitespace-pre break-all text-center font-mono text-xl font-bold tracking-[0.15em]"
      />
    </div>
  );
}

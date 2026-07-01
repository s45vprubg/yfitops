// Karaoke — reveal + synced lyrics (§8A, §6). Winner flashes; screen splits:
// top shows the revealed Artist + Song, bottom shows LRCLIB synced lyrics with
// the active line highlighted in real time.
//
// Lyric sync: the audio position comes from the AudioPlayer (Spotify SDK
// player_state_changed, or the mock's virtual playhead). We INTERPOLATE between
// events via a rAF loop reading audio.currentState() — which itself extrapolates
// position from the last snapshot — so the highlight glides smoothly even
// between sparse SDK events or dropped frames (§6).

import { useEffect, useRef, useState } from "react";
import type { GameState, LyricsData, RevealData } from "@shared/protocol";
import type { AudioPlayer } from "../audio";

interface Props {
  reveal: RevealData | null;
  lyrics: LyricsData | null;
  lyricsStatus: "idle" | "loading" | "ready" | "none";
  lockoutHandle: string | null;
  roundWinner: string | null;
  gameState: GameState;
  audio: React.RefObject<AudioPlayer | null>;
}

export default function Karaoke({ reveal, lyrics, lyricsStatus, lockoutHandle, roundWinner, gameState, audio }: Props) {
  const [activeIdx, setActiveIdx] = useState(-1);
  const containerRef = useRef<HTMLDivElement>(null);
  const scrollerRef = useRef<HTMLDivElement>(null);
  const lineRefs = useRef<(HTMLDivElement | null)[]>([]);

  const lines = lyrics?.lines ?? [];

  // rAF loop: read interpolated audio position, find the active lyric line.
  useEffect(() => {
    if (lines.length === 0) return;
    let raf = 0;
    let last = -1;
    const tick = () => {
      const player = audio.current;
      const posMs = player ? player.currentState().positionMs : 0;
      // Active line = last line whose timeMs <= posMs.
      let idx = -1;
      for (let i = 0; i < lines.length; i++) {
        if (lines[i].timeMs <= posMs) idx = i;
        else break;
      }
      if (idx !== last) {
        last = idx;
        setActiveIdx(idx);
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [lines, audio]);

  // Transform-based scroll (Apple-Music style): translate the whole lyric
  // column so the active line sits at a fixed focal point (~42% down). Animating
  // translateY is GPU-cheap and doesn't reflow, and measuring the active line's
  // real offset keeps centering correct even when a line wraps to two lines.
  useEffect(() => {
    const el = lineRefs.current[activeIdx];
    const scroller = scrollerRef.current;
    const container = containerRef.current;
    if (!scroller || !container) return;
    if (!el) {
      scroller.style.transform = "translateY(0px)";
      return;
    }
    const focal = container.clientHeight * 0.42;
    const y = focal - (el.offsetTop + el.offsetHeight / 2);
    scroller.style.transform = `translateY(${y}px)`;
  }, [activeIdx, lines]);

  const isAdjudicating = gameState === "ADJUDICATE";
  // Banner: while adjudicating show who's guessing; at karaoke show the ACTUAL
  // round winner (never the scoreboard leader). Empty winner => nobody got it.
  const nobodyWon = !isAdjudicating && !roundWinner;
  const bannerName = isAdjudicating ? lockoutHandle : roundWinner;

  return (
    <div className="flex h-full w-full flex-col">
      {/* Guesser/Winner banner */}
      {(bannerName || nobodyWon) && (
        <div className="flex flex-col items-center pt-8">
          <div className="text-sm uppercase tracking-[0.5em] text-neon-amber/70">
            {isAdjudicating ? "now guessing" : "winner"}
          </div>
          {nobodyWon ? (
            <div className="text-5xl font-extrabold text-neon-magenta/80">nobody :(</div>
          ) : (
            <div className="text-5xl font-extrabold text-neon-amber neon-text animate-winnerPop">{bannerName}</div>
          )}
        </div>
      )}

      {/* Top: revealed track */}
      <div className="flex flex-col items-center justify-center border-b border-neon-green/20 px-8 py-6">
        <div className="text-xs uppercase tracking-[0.6em] text-neon-cyan/40">artist</div>
        <div className="text-[clamp(2rem,5vw,4.5rem)] font-extrabold text-neon-green neon-text">
          {reveal?.artist ?? "—"}
        </div>
        <div className="mt-2 text-xs uppercase tracking-[0.6em] text-neon-cyan/40">song</div>
        <div className="text-[clamp(1.5rem,4vw,3.5rem)] font-bold text-neon-cyan neon-cyan">{reveal?.song ?? "—"}</div>
      </div>

      {/* Bottom: synced lyrics */}
      <div ref={containerRef} className="relative flex-1 overflow-hidden px-8">
        {lines.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-4 text-neon-cyan/40">
            {lyricsStatus === "none" ? (
              <div className="text-2xl text-neon-cyan/30">no synced lyrics for this track</div>
            ) : (
              <>
                <div className="h-10 w-10 animate-spin rounded-full border-2 border-neon-cyan/20 border-t-neon-cyan/80" />
                <div className="text-xl tracking-[0.3em]">loading lyrics…</div>
              </>
            )}
          </div>
        ) : (
          // Transform-scrolled column: translateY animates smoothly (GPU, no
          // reflow). Every line keeps a STABLE box (fixed font-size + line
          // height); the active line is emphasized only with color/opacity and a
          // transform scale, so nothing below it shifts when it "grows".
          <div
            ref={scrollerRef}
            className="mx-auto flex max-w-4xl flex-col items-center gap-5 will-change-transform"
            style={{ transition: "transform 500ms cubic-bezier(0.22, 0.61, 0.36, 1)" }}
          >
            {lines.map((l, i) => {
              const active = i === activeIdx;
              const past = i < activeIdx;
              return (
                <div
                  key={i}
                  ref={(el) => {
                    lineRefs.current[i] = el;
                  }}
                  className={[
                    "origin-center text-center text-[clamp(1.5rem,2.6vw,2.4rem)] font-bold leading-snug",
                    "transition-[opacity,transform,color] duration-500 ease-out",
                    active
                      ? "scale-[1.14] text-neon-green neon-text opacity-100"
                      : past
                        ? "scale-100 text-neon-cyan/25 opacity-60"
                        : "scale-100 text-neon-cyan/55 opacity-80",
                  ].join(" ")}
                >
                  {l.text || "♪"}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

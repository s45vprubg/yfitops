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
import type { LyricsData, RevealData, ScoreboardData } from "@shared/protocol";
import type { AudioPlayer } from "../audio";

interface Props {
  reveal: RevealData | null;
  lyrics: LyricsData | null;
  scoreboard: ScoreboardData | null;
  lockoutHandle: string | null;
  audio: React.RefObject<AudioPlayer | null>;
}

export default function Karaoke({ reveal, lyrics, scoreboard, lockoutHandle, audio }: Props) {
  const [activeIdx, setActiveIdx] = useState(-1);
  const containerRef = useRef<HTMLDivElement>(null);
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

  // Keep the active line scrolled to center.
  useEffect(() => {
    const el = lineRefs.current[activeIdx];
    if (el && containerRef.current) {
      el.scrollIntoView({ behavior: "smooth", block: "center" });
    }
  }, [activeIdx]);

  const topGuesser = lockoutHandle ?? scoreboard?.players?.[0]?.handle ?? null;

  return (
    <div className="flex h-full w-full flex-col">
      {/* Winner flash */}
      {topGuesser && (
        <div className="flex flex-col items-center pt-8">
          <div className="text-sm uppercase tracking-[0.5em] text-neon-amber/70">winner</div>
          <div className="text-5xl font-extrabold text-neon-amber neon-text animate-winnerPop">{topGuesser}</div>
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
      <div ref={containerRef} className="relative flex-1 overflow-hidden px-8 py-10">
        {lines.length === 0 ? (
          <div className="flex h-full items-center justify-center text-2xl text-neon-cyan/30">
            no synced lyrics for this track
          </div>
        ) : (
          <div className="mx-auto flex max-w-4xl flex-col items-center gap-4">
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
                    "text-center transition-all duration-200",
                    active
                      ? "scale-110 text-[clamp(1.8rem,3.5vw,3rem)] font-extrabold text-neon-green neon-text"
                      : past
                        ? "text-[clamp(1.2rem,2vw,1.8rem)] text-neon-cyan/30"
                        : "text-[clamp(1.2rem,2vw,1.8rem)] text-neon-cyan/60",
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

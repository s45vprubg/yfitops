// Lobby — massive centered QR encoding the mobile join URL, plus a rolling
// ticker of joined usernames sourced from the scoreboard payload (§8A).

import type { ScoreboardData } from "@shared/protocol";
import { JOIN_URL } from "../config";
import { useQrDataUrl } from "../components/CornerJoin";

export default function Lobby({ scoreboard }: { scoreboard: ScoreboardData | null }) {
  const qr = useQrDataUrl(JOIN_URL, 720);
  const players = scoreboard?.players ?? [];
  const handles = players.map((p) => p.handle);
  // Duplicate the list so the marquee scroll is seamless.
  const ticker = handles.length > 0 ? [...handles, ...handles] : [];

  return (
    <div className="flex h-full w-full flex-col items-center justify-center px-8">
      <h1 className="mb-2 text-5xl font-extrabold tracking-[0.3em] text-neon-green neon-text animate-pulseGlow">
        yfitops
      </h1>
      <p className="mb-8 text-lg uppercase tracking-[0.5em] text-neon-cyan/70">scan to jack in</p>

      <div className="relative rounded-2xl border-2 border-neon-green/40 bg-panel p-6 shadow-[0_0_60px_rgba(53,255,148,0.25)]">
        {qr ? (
          <img src={qr} alt="Join game QR code" className="h-[42vh] max-h-[560px] w-auto" />
        ) : (
          <div className="h-[42vh] max-h-[560px] w-[42vh] max-w-[560px] animate-pulse rounded bg-neon-green/10" />
        )}
        {/* sweeping scan line */}
        <div className="pointer-events-none absolute inset-x-6 top-6 h-[42vh] max-h-[560px] overflow-hidden">
          <div className="h-px w-full bg-neon-green/70 shadow-[0_0_12px_2px_rgba(53,255,148,0.8)] animate-scan" />
        </div>
      </div>

      <div className="mt-6 text-2xl font-semibold text-neon-green neon-text">{JOIN_URL.replace(/^https?:\/\//, "")}</div>

      <div className="mt-10 w-full max-w-5xl overflow-hidden">
        <div className="mb-2 text-center text-sm uppercase tracking-[0.4em] text-neon-cyan/60">
          {handles.length} jacked in
        </div>
        {ticker.length > 0 ? (
          <div className="relative w-full overflow-hidden">
            <div className="flex w-max gap-8 whitespace-nowrap" style={tickerStyle}>
              {ticker.map((h, i) => (
                <span key={i} className="text-3xl font-bold text-neon-cyan neon-cyan">
                  {h}
                </span>
              ))}
            </div>
          </div>
        ) : (
          <div className="text-center text-2xl text-neon-cyan/40">waiting for players…</div>
        )}
      </div>
    </div>
  );
}

// Horizontal marquee. We reuse the keyframe by translating X via inline anim.
const tickerStyle: React.CSSProperties = {
  animationName: "marquee",
  animationDuration: "18s",
  animationTimingFunction: "linear",
  animationIterationCount: "infinite",
};

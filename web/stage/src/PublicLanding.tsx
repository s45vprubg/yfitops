// PublicLanding — what yfitops.us/ shows to anyone who loads it. Deliberately
// inert: no useGame(), no secret, no privileged connection of any kind. Just
// the QR code to join and a live player count from a dedicated unauthenticated
// endpoint (/api/public/player-count carries nothing but a bare integer — no
// handles, no scores, no track data, so it's safe without any auth).
//
// The real stage (game board + audio control) lives at /stage behind a login
// — see StageApp (App.tsx) + Login.tsx.

import { useEffect, useState } from "react";
import { useQrDataUrl } from "./components/CornerJoin";
import { JOIN_URL, HTTP_URL } from "./config";

const POLL_MS = 5000;

function usePlayerCount(): number | null {
  const [count, setCount] = useState<number | null>(null);
  useEffect(() => {
    let alive = true;
    const poll = async () => {
      try {
        const res = await fetch(`${HTTP_URL}/api/public/player-count`, { cache: "no-store" });
        if (!res.ok) return;
        const body = (await res.json()) as { count?: number };
        if (alive && typeof body.count === "number") setCount(body.count);
      } catch {
        // offline/unreachable — leave the last known count showing rather than
        // flicker to an error state for a non-critical display.
      }
    };
    void poll();
    const id = setInterval(poll, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, []);
  return count;
}

export default function PublicLanding() {
  const qr = useQrDataUrl(JOIN_URL, 720);
  const count = usePlayerCount();

  return (
    <div className="flex h-full w-full flex-col items-center justify-center bg-[#05070a] px-8">
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
      </div>

      <div className="mt-6 text-2xl font-semibold text-neon-green neon-text">
        {JOIN_URL.replace(/^https?:\/\//, "")}
      </div>

      {count !== null && (
        <div className="mt-10 text-sm uppercase tracking-[0.4em] text-neon-cyan/60">{count} jacked in</div>
      )}
    </div>
  );
}

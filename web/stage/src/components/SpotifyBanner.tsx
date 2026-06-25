// SpotifyBanner — honest status of the audio layer. Status-only; the Connect
// Spotify action lives in the Admin control room. Anchored top-RIGHT so it sits
// out of the way of the centered stage content.

import type { ConnectState, AudioMode } from "../audio";

const anchor = "fixed top-4 right-4 z-40";

export default function SpotifyBanner({ mode, state }: { mode: AudioMode; state: ConnectState }) {
  // Connected/ready: a compact "LIVE / tunes" badge (the audio source is live).
  if (mode === "spotify" && state === "ready") {
    return (
      <div className={`${anchor} flex flex-col items-end gap-0.5`}>
        <span className="flex items-center gap-1.5 text-[10px] font-bold uppercase tracking-widest text-neon-green">
          <span className="inline-block h-2 w-2 animate-pulse rounded-full bg-neon-green" />
          Live
        </span>
        <span className="rounded-md border border-neon-green/40 bg-panel/80 px-3 py-1 text-xs font-semibold text-neon-green backdrop-blur">
          tunes
        </span>
      </div>
    );
  }

  if (mode === "spotify" && state === "connecting") {
    return (
      <div className={`${anchor} rounded-md border border-neon-amber/40 bg-panel/80 px-3 py-1 text-xs text-neon-amber backdrop-blur`}>
        ◌ Connecting tunes…
      </div>
    );
  }

  const errored = mode === "spotify" && state === "error";
  return (
    <div className={`${anchor} rounded-md border border-neon-magenta/40 bg-panel/80 px-3 py-1 text-xs text-neon-magenta backdrop-blur`}>
      {errored ? "Audio auth failed — demo mode" : "Demo mode — connect Spotify from Admin"}
    </div>
  );
}

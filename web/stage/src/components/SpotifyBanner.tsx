// SpotifyBanner — honest status of the audio layer. Status-only; the Connect
// Spotify action lives in the Admin control room.

import type { ConnectState, AudioMode } from "../audio";

export default function SpotifyBanner({ mode, state }: { mode: AudioMode; state: ConnectState }) {
  if (mode === "spotify" && state === "ready") {
    return (
      <div className="fixed top-4 left-4 z-40 rounded-md border border-neon-green/40 bg-panel/80 px-3 py-1 text-xs text-neon-green backdrop-blur">
        ● Spotify device ready
      </div>
    );
  }

  if (mode === "spotify" && state === "connecting") {
    return (
      <div className="fixed top-4 left-4 z-40 rounded-md border border-neon-amber/40 bg-panel/80 px-3 py-1 text-xs text-neon-amber backdrop-blur">
        ◌ Connecting Spotify…
      </div>
    );
  }

  const errored = mode === "spotify" && state === "error";
  return (
    <div className="fixed top-4 left-4 z-40 rounded-md border border-neon-magenta/40 bg-panel/80 px-3 py-1 text-xs text-neon-magenta backdrop-blur">
      {errored ? "Spotify auth failed — demo mode" : "Demo mode — connect Spotify from Admin"}
    </div>
  );
}

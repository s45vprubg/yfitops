// SpotifyBanner — honest status of the audio layer. When no token is present we
// run in demo mode and say so plainly; the rest of the stage works regardless.
// Offers a "Connect Spotify" action that redirects to the backend OAuth flow.

import type { ConnectState, AudioMode } from "../audio";
import { SPOTIFY_AUTH_URL } from "../config";

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

  const connect = () => {
    window.location.href = SPOTIFY_AUTH_URL;
  };

  const errored = mode === "spotify" && state === "error";
  return (
    <div className="fixed top-4 left-4 z-40 flex items-center gap-3 rounded-md border border-neon-magenta/40 bg-panel/80 px-3 py-1 text-xs text-neon-magenta backdrop-blur">
      <span>{errored ? "Spotify auth failed — demo mode" : "Spotify not connected — demo mode"}</span>
      <button
        onClick={connect}
        className="rounded border border-neon-green/50 px-2 py-0.5 text-neon-green transition hover:bg-neon-green/10"
      >
        Connect Spotify
      </button>
    </div>
  );
}

// SoloKaraokeView — the stage's screen for the second, independent game mode:
// straight admin-curated playback, no buzzing/guessing/scoring. Reuses
// Karaoke's lyric-sync engine wholesale via `hideBanner` (the winner/guesser
// banner has no meaning here — same reuse pattern DailyDoublePerformance
// already uses via `label`). Lyric lines arrive over the existing shared
// `lyrics` message, exactly like trivia's karaoke phase (see useGame.ts).
//
// Karaoke.tsx already renders the revealed artist/song from `reveal`, so the
// only thing it doesn't show that Solo Karaoke needs is the album art + queue
// position — added here as a thin overlay rather than touching Karaoke.tsx.

import type { LyricsData, SoloKaraokeNowPlayingData } from "@shared/protocol";
import type { AudioPlayer } from "../audio";
import Karaoke from "./Karaoke";

interface Props {
  // Null (or index === -1) means nothing is loaded yet — e.g. Solo Karaoke
  // was just activated but the admin hasn't pressed play on a track.
  nowPlaying: SoloKaraokeNowPlayingData | null;
  lyrics: LyricsData | null;
  lyricsStatus: "idle" | "loading" | "ready" | "none";
  audio: React.RefObject<AudioPlayer | null>;
}

export default function SoloKaraokeView({ nowPlaying, lyrics, lyricsStatus, audio }: Props) {
  if (!nowPlaying || nowPlaying.index < 0) {
    return (
      <div className="flex h-full w-full flex-col items-center justify-center gap-4">
        <div className="text-6xl">🎤</div>
        <div className="text-sm uppercase tracking-[0.5em] text-neon-amber/70">solo karaoke</div>
        <div className="text-lg text-neon-cyan/60">waiting for a track…</div>
      </div>
    );
  }

  const reveal = { artist: nowPlaying.artist, song: nowPlaying.song, albumArt: nowPlaying.albumArt };

  return (
    <div className="relative h-full w-full">
      {nowPlaying.albumArt && (
        <img
          src={nowPlaying.albumArt}
          alt=""
          className="absolute left-8 top-8 z-10 h-24 w-24 rounded-lg border border-neon-green/30 object-cover shadow-[0_0_20px_rgba(53,255,148,0.25)]"
        />
      )}
      {nowPlaying.queueLen > 0 && (
        <div className="absolute right-8 top-8 z-10 text-xs uppercase tracking-[0.4em] text-neon-cyan/40">
          track {nowPlaying.index + 1} / {nowPlaying.queueLen}
        </div>
      )}
      <Karaoke
        reveal={reveal}
        lyrics={lyrics}
        lyricsStatus={lyricsStatus}
        lockoutHandle={null}
        roundWinner={null}
        gameState="LOBBY"
        audio={audio}
        hideBanner
      />
    </div>
  );
}

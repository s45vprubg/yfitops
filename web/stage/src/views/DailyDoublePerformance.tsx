// DailyDoublePerformance — the performer's song plays with full metadata +
// synced lyrics shown openly (no masking, no decrypt animation: the crowd
// rates instead of guessing). Reuses Karaoke's lyric-sync engine wholesale
// rather than re-implementing it.

import type { LyricsData, RevealData } from "@shared/protocol";
import type { AudioPlayer } from "../audio";
import Karaoke from "./Karaoke";

interface Props {
  reveal: RevealData | null;
  lyrics: LyricsData | null;
  lyricsStatus: "idle" | "loading" | "ready" | "none";
  performerHandle: string;
  audio: React.RefObject<AudioPlayer | null>;
}

export default function DailyDoublePerformance({ reveal, lyrics, lyricsStatus, performerHandle, audio }: Props) {
  return (
    <Karaoke
      reveal={reveal}
      lyrics={lyrics}
      lyricsStatus={lyricsStatus}
      lockoutHandle={null}
      roundWinner={performerHandle}
      gameState="DAILY_DOUBLE"
      audio={audio}
      label="daily double"
    />
  );
}

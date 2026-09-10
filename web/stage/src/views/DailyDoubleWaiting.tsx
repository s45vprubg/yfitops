// DailyDoubleWaiting — shown while the contestant is deciding accept/decline
// or picking a song, before any track starts. No track metadata exists yet
// for this cell (§4A) — just the sanitized handle.

interface Props {
  handle: string;
}

export default function DailyDoubleWaiting({ handle }: Props) {
  return (
    <div className="flex h-full w-full flex-col items-center justify-center gap-4">
      <div className="text-6xl">🎤</div>
      <div className="text-sm uppercase tracking-[0.5em] text-neon-amber/70">daily double</div>
      <div className="text-5xl font-extrabold text-neon-amber neon-text animate-winnerPop">{handle}</div>
      <div className="text-lg text-neon-cyan/60">deciding…</div>
    </div>
  );
}

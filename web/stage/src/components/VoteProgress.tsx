// VoteProgress — a compact skip-vote readout for the projector during
// KARAOKE, pinned top-left (the same corner ScoreOverlay would use, but
// ScoreOverlay is deliberately excluded from this state — see App.tsx's
// SCORE_OVERLAY_STATES — so the corner is free). Shows "{have}/{active}"
// plus a bar filled to have/active with a threshold-line marker at
// need/active, so the room can see how many more skip votes are needed.
// Formatted to match ScoreOverlay's panel styling (owner request).

export default function VoteProgress({
  progress,
}: {
  progress: { have: number; need: number; active: number } | null;
}) {
  if (!progress || progress.active === 0) return null;
  const { have, need, active } = progress;
  const havePct = Math.min(100, (have / active) * 100);
  const needPct = Math.min(100, (need / active) * 100);

  return (
    <div className="fixed top-4 left-4 z-30 w-64 rounded-lg border border-neon-cyan/25 bg-panel/80 p-3 backdrop-blur-sm">
      <div className="mb-2 flex items-center justify-between text-[10px] uppercase tracking-[0.4em] text-neon-cyan/50">
        <span>skip votes</span>
        <span className="tnum text-neon-amber">{have}/{active}</span>
      </div>
      <div className="relative h-2 w-full overflow-hidden rounded-full bg-neon-cyan/10">
        <div
          className="h-full rounded-full bg-neon-green/70"
          style={{ width: `${havePct}%` }}
        />
        <div
          className="absolute top-0 h-full w-[2px] bg-neon-amber"
          style={{ left: `${needPct}%` }}
          title={`${need} needed`}
        />
      </div>
    </div>
  );
}

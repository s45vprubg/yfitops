// ScoreOverlay — a compact persistent standings panel for the projector.
// Shown during the board/round flow so the room always sees scores without
// waiting for GAME_OVER. Handles + scores only, top 3 (enough to see who's
// leading without competing for room with the board/join box).
//
// Pinned top-left as a floating overlay by default. On the Board/Transition
// view specifically, Board.tsx renders this with `inline` instead, as part
// of a normal-flow header row above the full-width grid (too cramped as a
// floating overlay once the grid also needs that same corner).

import type { ScoreboardData } from "@shared/protocol";

export default function ScoreOverlay({
  scoreboard,
  inline = false,
}: {
  scoreboard: ScoreboardData | null;
  inline?: boolean;
}) {
  const players = [...(scoreboard?.players ?? [])].sort((a, b) => b.score - a.score).slice(0, 3);
  if (players.length === 0) return null;

  return (
    <div
      className={[
        inline ? "w-72" : "fixed top-4 left-4 z-30 w-64",
        "rounded-lg border border-neon-cyan/25 bg-panel/80 p-3 backdrop-blur-sm",
      ].join(" ")}
    >
      <div className="mb-2 text-center text-[10px] uppercase tracking-[0.4em] text-neon-cyan/50">
        scores
      </div>
      <ol className="flex flex-col gap-1">
        {players.map((p, i) => (
          <li key={p.id} className="flex items-center justify-between text-sm">
            <span className="flex items-center gap-2 truncate">
              <span className="tnum w-4 shrink-0 text-right text-neon-cyan/40">{i + 1}</span>
              <span className="truncate font-semibold text-neon-green/90">{p.handle}</span>
            </span>
            <span className="tnum ml-2 shrink-0 font-bold text-neon-amber">{p.score}</span>
          </li>
        ))}
      </ol>
    </div>
  );
}

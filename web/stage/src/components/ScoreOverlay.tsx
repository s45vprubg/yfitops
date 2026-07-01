// ScoreOverlay — a compact persistent standings panel for the projector,
// pinned bottom-left. Shown during the board/round flow so the room always sees
// scores without waiting for GAME_OVER. Handles + scores only.

import type { ScoreboardData } from "@shared/protocol";

export default function ScoreOverlay({ scoreboard }: { scoreboard: ScoreboardData | null }) {
  const players = [...(scoreboard?.players ?? [])].sort((a, b) => b.score - a.score).slice(0, 8);
  if (players.length === 0) return null;

  return (
    <div className="fixed bottom-4 left-4 z-30 w-64 rounded-lg border border-neon-cyan/25 bg-panel/80 p-3 backdrop-blur-sm">
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

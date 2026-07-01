import type { ScoreboardData } from "@shared/protocol";

// Scoreboard — compact standings for the phone. Shows handles + scores ranked
// high-to-low. Handles + scores only (no track metadata), so it's §4A-safe.
// `me` highlights this player's own row if their handle is known.
export function Scoreboard({ scoreboard, me }: { scoreboard: ScoreboardData | null; me?: string | null }) {
  const players = [...(scoreboard?.players ?? [])].sort((a, b) => b.score - a.score);
  if (players.length === 0) return null;

  return (
    <div className="w-full max-w-sm rounded-2xl border border-neutral-800 bg-panel p-4">
      <div className="mb-2 text-center text-xs uppercase tracking-[0.3em] text-neutral-500">
        Scores
      </div>
      <ol className="flex flex-col gap-1">
        {players.map((p, i) => {
          const mine = !!me && p.handle === me;
          return (
            <li
              key={p.id}
              className={[
                "flex items-center justify-between rounded-lg px-3 py-2 text-sm",
                mine ? "bg-guess/15 text-guess" : "text-neutral-200",
              ].join(" ")}
            >
              <span className="flex items-center gap-2 truncate">
                <span className="w-5 shrink-0 text-right font-mono text-neutral-500">{i + 1}</span>
                <span className="truncate font-medium">{p.handle}</span>
              </span>
              <span className="ml-2 shrink-0 font-mono font-bold tabular-nums">{p.score}</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

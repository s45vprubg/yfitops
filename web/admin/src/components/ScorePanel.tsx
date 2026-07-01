import type { ScoreEntry } from "@shared/protocol";
import { Empty, PanelHead } from "./BoardPanel";

interface Props {
  players: ScoreEntry[];
}

// Ranked scoreboard for the control room. Shares the right column with the
// telemetry panel (split vertically). Sorted high-to-low.
export default function ScorePanel({ players }: Props) {
  const ranked = [...players].sort((a, b) => b.score - a.score);

  return (
    <section className="flex min-h-0 flex-col border-l border-t border-edge bg-panel2">
      <PanelHead title="Scoreboard" hint={`${ranked.length} player${ranked.length === 1 ? "" : "s"}`} />
      <div className="flex-1 overflow-auto">
        {ranked.length === 0 ? (
          <Empty>No players yet</Empty>
        ) : (
          <ol className="flex flex-col">
            {ranked.map((p, i) => (
              <li
                key={p.id}
                className="flex items-center justify-between border-b border-edge/50 px-3 py-2 text-sm"
              >
                <span className="flex items-center gap-2 truncate">
                  <span className="w-5 shrink-0 text-right font-mono text-slate-500">{i + 1}</span>
                  <span className="truncate text-slate-200">{p.handle}</span>
                </span>
                <span className="ml-2 shrink-0 font-mono font-semibold text-accent">{p.score}</span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </section>
  );
}

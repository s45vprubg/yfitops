import { useState } from "react";
import type { ScoreEntry } from "@shared/protocol";
import { Empty, PanelHead } from "./BoardPanel";
import type { AdminActions } from "../useAdmin";
import { useModal } from "./Modal";

interface Props {
  players: ScoreEntry[];
  actions: AdminActions;
}

// Ranked scoreboard for the control room. Shares the right column with the
// telemetry panel (split vertically). Sorted high-to-low.
export default function ScorePanel({ players, actions }: Props) {
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
              <ScoreRow key={p.id} player={p} rank={i + 1} actions={actions} />
            ))}
          </ol>
        )}
      </div>
    </section>
  );
}

function ScoreRow({
  player,
  rank,
  actions,
}: {
  player: ScoreEntry;
  rank: number;
  actions: AdminActions;
}) {
  const { promptText } = useModal();
  const [menuOpen, setMenuOpen] = useState(false);

  const editPoints = async () => {
    setMenuOpen(false);
    const raw = await promptText({
      title: `Edit points — ${player.handle}`,
      body: `Current score: ${player.score}. Enter points to add or remove (e.g. 50 or -25).`,
      placeholder: "±pts",
    });
    if (raw === null) return;
    const delta = Number(raw);
    if (!Number.isFinite(delta) || delta === 0) return;
    actions.award({ playerID: player.id, delta });
  };

  return (
    <li className="relative flex items-center justify-between border-b border-edge/50 px-3 py-2 text-sm">
      <span className="flex items-center gap-2 truncate">
        <span className="w-5 shrink-0 text-right font-mono text-slate-500">{rank}</span>
        <span className="truncate text-slate-200">{player.handle}</span>
      </span>
      <span className="ml-2 flex shrink-0 items-center gap-1.5">
        <span className="font-mono font-semibold text-accent">{player.score}</span>
        <button
          onClick={() => setMenuOpen((o) => !o)}
          className="rounded px-1 text-slate-500 hover:bg-panel3 hover:text-slate-200"
          title="Player actions"
        >
          ⋮
        </button>
      </span>
      {menuOpen && (
        <div className="absolute right-3 top-full z-10 mt-1 min-w-[8rem] rounded border border-edge bg-panel3 p-1 shadow-xl">
          <button
            onClick={editPoints}
            className="w-full rounded px-2 py-1.5 text-left text-xs font-semibold text-slate-200 hover:bg-panel2 hover:text-accent"
          >
            Edit points
          </button>
        </div>
      )}
    </li>
  );
}

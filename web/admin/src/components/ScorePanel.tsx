import { useEffect, useState } from "react";
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
  // Lifted up (dd-ui-admin-4) so only one row's ⋮ menu can be open at a time,
  // instead of each ScoreRow owning independent local state.
  const [openMenuId, setOpenMenuId] = useState<string | null>(null);

  // Close the open menu on any click outside it. Rows stop propagation on
  // their own toggle button's mousedown (see ScoreRow) so this doesn't fight
  // with clicking that same button to close its own menu.
  useEffect(() => {
    if (openMenuId === null) return;
    const onOutside = () => setOpenMenuId(null);
    document.addEventListener("mousedown", onOutside);
    return () => document.removeEventListener("mousedown", onOutside);
  }, [openMenuId]);

  return (
    <section className="flex min-h-0 flex-col border-l border-t border-edge bg-panel2">
      <PanelHead title="Scoreboard" hint={`${ranked.length} player${ranked.length === 1 ? "" : "s"}`} />
      <div className="flex-1 overflow-auto">
        {ranked.length === 0 ? (
          <Empty>No players yet</Empty>
        ) : (
          <ol className="flex flex-col">
            {ranked.map((p, i) => (
              <ScoreRow
                key={p.id}
                player={p}
                rank={i + 1}
                actions={actions}
                menuOpen={openMenuId === p.id}
                onToggleMenu={() => setOpenMenuId((cur) => (cur === p.id ? null : p.id))}
                onCloseMenu={() => setOpenMenuId(null)}
              />
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
  menuOpen,
  onToggleMenu,
  onCloseMenu,
}: {
  player: ScoreEntry;
  rank: number;
  actions: AdminActions;
  menuOpen: boolean;
  onToggleMenu: () => void;
  onCloseMenu: () => void;
}) {
  const { promptText } = useModal();
  // dd-ui-admin-5: invalid/zero input used to silently no-op with no
  // feedback. Surface it inline, matching BoardBuilderPage's dismissable
  // error banner convention.
  const [pointsError, setPointsError] = useState<string | null>(null);

  const editPoints = async () => {
    onCloseMenu();
    setPointsError(null);
    const raw = await promptText({
      title: `Edit points — ${player.handle}`,
      body: `Current score: ${player.score}. Enter points to add or remove (e.g. 50 or -25).`,
      placeholder: "±pts",
    });
    if (raw === null) return;
    const delta = Number(raw);
    if (!Number.isFinite(delta) || delta === 0) {
      setPointsError(`"${raw}" isn't a valid non-zero number — no change applied.`);
      return;
    }
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
          onMouseDown={(e) => e.stopPropagation()}
          onClick={onToggleMenu}
          className="rounded px-1 text-slate-500 hover:bg-panel3 hover:text-slate-200"
          title="Player actions"
        >
          ⋮
        </button>
      </span>
      {menuOpen && (
        <div
          onMouseDown={(e) => e.stopPropagation()}
          className="absolute right-3 top-full z-10 mt-1 min-w-[8rem] rounded border border-edge bg-panel3 p-1 shadow-xl"
        >
          <button
            onClick={editPoints}
            className="w-full rounded px-2 py-1.5 text-left text-xs font-semibold text-slate-200 hover:bg-panel2 hover:text-accent"
          >
            Edit points
          </button>
        </div>
      )}
      {pointsError && (
        <div className="absolute right-3 top-full z-20 mt-1 flex max-w-[14rem] items-start gap-1.5 rounded border border-red-900/60 bg-red-950/90 px-2 py-1 text-[11px] text-red-300 shadow-xl">
          <span className="flex-1">{pointsError}</span>
          <button
            onClick={() => setPointsError(null)}
            className="font-semibold text-red-400 hover:text-white"
          >
            ×
          </button>
        </div>
      )}
    </li>
  );
}

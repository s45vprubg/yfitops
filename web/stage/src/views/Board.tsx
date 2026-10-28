// Board — clean Jeopardy-style grid from the board payload (§8A). Top row =
// categories; cells show point values; exhausted cells faded.
//
// Renders its own header row (leaderboard + join QR, both `inline`) above
// the grid instead of leaving App.tsx's floating corner overlays on top of
// it — a fixed-position leaderboard/QR over a full-width grid had no room
// that didn't collide with a column, so App.tsx suppresses those floating
// overlays specifically while Board is on screen and lets Board compose its
// own header instead. The grid itself is full width, no side padding.

import type { BoardData, BoardCell, ScoreboardData } from "@shared/protocol";
import ScoreOverlay from "../components/ScoreOverlay";
import CornerJoin from "../components/CornerJoin";

export default function Board({
  board,
  highlightCell,
  scoreboard,
}: {
  board: BoardData | null;
  // The cell the picker has queued for the next round (smsgPendingPick,
  // CONTRACT-QUESTION — see engine.go), so the projector can show it's
  // already decided during the between-rounds countdown. Optional/additive:
  // undefined renders exactly as before.
  highlightCell?: { row: number; col: number };
  scoreboard?: ScoreboardData | null;
}) {
  if (!board) {
    return <div className="flex h-full items-center justify-center text-2xl text-neon-green/50">loading board…</div>;
  }

  const { rows, cols, cells } = board;
  // Clamp dimensions before they drive Array.from({length}) — a corrupt frame
  // with huge rows/cols is a synchronous OOM the ErrorBoundary can't catch.
  // A real board is at most 5 rows and the admin caps columns at 8, so 12
  // comfortably exceeds anything legitimate.
  const MAX_DIM = 12;
  const r = Math.min(Math.max(Math.floor(rows) || 0, 0), MAX_DIM);
  const c = Math.min(Math.max(Math.floor(cols) || 0, 0), MAX_DIM);
  const byKey = new Map<string, BoardCell>();
  // Guard against a malformed frame (missing/non-array cells) white-screening
  // the projector — iterate defensively.
  for (const cell of cells ?? []) byKey.set(`${cell.row}:${cell.col}`, cell);

  // Categories: take the category string from the first cell in each column.
  // Columns are 1-indexed from the server.
  const categories: string[] = [];
  for (let col = 1; col <= c; col++) {
    let cat = "";
    for (let row = 1; row <= r; row++) {
      const cell = byKey.get(`${row}:${col}`);
      if (cell?.category) {
        cat = cell.category;
        break;
      }
    }
    categories.push(cat || `CAT ${col}`);
  }

  return (
    <div className="flex h-full w-full flex-col px-8 py-6">
      <div className="mb-4 flex items-start justify-between gap-4">
        <ScoreOverlay scoreboard={scoreboard ?? null} inline />
        <h2 className="flex-1 pt-3 text-center text-3xl font-bold uppercase tracking-[0.4em] text-neon-cyan neon-cyan">
          select a track
        </h2>
        <CornerJoin inline />
      </div>
      <div className="grid flex-1 gap-3" style={{ gridTemplateColumns: `repeat(${c}, minmax(0, 1fr))` }}>
        {categories.map((cat, col) => (
          <div
            key={`cat-${col}`}
            className="flex items-center justify-center rounded-lg border border-neon-green/30 bg-neon-green/5 px-2 py-4 text-center text-xl font-bold uppercase leading-tight tracking-wider text-neon-green"
          >
            {cat}
          </div>
        ))}

        {Array.from({ length: r }).map((_, rIdx) => {
          const row = rIdx + 1;
          return Array.from({ length: c }).map((__, cIdx) => {
            const col = cIdx + 1;
            const cell = byKey.get(`${row}:${col}`);
            const exhausted = cell?.exhausted ?? false;
            const points = cell?.points ?? 0;
            const isHighlighted = !exhausted && highlightCell?.row === row && highlightCell?.col === col;
            return (
              <div
                key={`${row}:${col}`}
                className={[
                  "flex flex-col items-center justify-center rounded-lg border transition",
                  exhausted
                    ? "border-white/5 bg-white/[0.02] text-white/15"
                    : "border-neon-cyan/30 bg-panel text-neon-amber shadow-[0_0_24px_rgba(255,176,0,0.12)]",
                  isHighlighted ? "animate-pulse ring-2 ring-neon-magenta shadow-[0_0_28px_rgba(255,43,214,0.6)]" : "",
                ].join(" ")}
              >
                <span className={["tnum font-extrabold", exhausted ? "text-4xl" : "text-5xl neon-text"].join(" ")}>
                  {exhausted ? "✕" : points}
                </span>
                {!exhausted && cell && (
                  <span className="mt-1 text-[10px] uppercase tracking-widest text-neon-cyan/40">
                    {cell.tracksLeft} left
                  </span>
                )}
              </div>
            );
          });
        })}
      </div>
    </div>
  );
}

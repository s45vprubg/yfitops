// Board — clean Jeopardy-style grid from the board payload (§8A). Top row =
// categories; cells show point values; exhausted cells faded.

import type { BoardData, BoardCell } from "@shared/protocol";

export default function Board({ board }: { board: BoardData | null }) {
  if (!board) {
    return <div className="flex h-full items-center justify-center text-2xl text-neon-green/50">loading board…</div>;
  }

  const { rows, cols, cells } = board;
  // Index cells by row/col. Row 0 (or absent) = category headers; data rows 1..rows.
  const byKey = new Map<string, BoardCell>();
  for (const c of cells) byKey.set(`${c.row}:${c.col}`, c);

  // Categories: take the category string from the first cell in each column.
  const categories: string[] = [];
  for (let col = 0; col < cols; col++) {
    let cat = "";
    for (let r = 1; r <= rows; r++) {
      const c = byKey.get(`${r}:${col}`);
      if (c?.category) {
        cat = c.category;
        break;
      }
    }
    categories.push(cat || `CAT ${col + 1}`);
  }

  return (
    <div className="flex h-full w-full flex-col px-10 py-12">
      <h2 className="mb-6 text-center text-3xl font-bold uppercase tracking-[0.4em] text-neon-cyan neon-cyan">
        select a track
      </h2>
      <div className="grid flex-1 gap-3" style={{ gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }}>
        {categories.map((cat, col) => (
          <div
            key={`cat-${col}`}
            className="flex items-center justify-center rounded-lg border border-neon-green/30 bg-neon-green/5 px-2 py-4 text-center text-xl font-bold uppercase leading-tight tracking-wider text-neon-green"
          >
            {cat}
          </div>
        ))}

        {Array.from({ length: rows }).map((_, rIdx) => {
          const row = rIdx + 1;
          return Array.from({ length: cols }).map((__, col) => {
            const cell = byKey.get(`${row}:${col}`);
            const exhausted = cell?.exhausted ?? false;
            const points = cell?.points ?? 0;
            return (
              <div
                key={`${row}:${col}`}
                className={[
                  "flex flex-col items-center justify-center rounded-lg border transition",
                  exhausted
                    ? "border-white/5 bg-white/[0.02] text-white/15"
                    : "border-neon-cyan/30 bg-panel text-neon-amber shadow-[0_0_24px_rgba(255,176,0,0.12)]",
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

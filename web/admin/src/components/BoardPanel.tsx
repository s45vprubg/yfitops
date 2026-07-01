import type { ReactNode } from "react";
import type { BoardCell, BoardData, GameState } from "@shared/protocol";

interface Props {
  board?: BoardData;
  gameState?: GameState;
  spotifyConnected: boolean;
  onSelect: (row: number, col: number) => void;
}

const SELECTABLE_STATES: GameState[] = [
  "BOARD",
  "KARAOKE",
  "TRANSITION",
];

function canSelectCell(gameState?: GameState, spotifyConnected?: boolean): boolean {
  if (!gameState || !SELECTABLE_STATES.includes(gameState)) return false;
  if (!spotifyConnected) return false;
  return true;
}

export default function BoardPanel({ board, gameState, spotifyConnected, onSelect }: Props) {
  const selectable = canSelectCell(gameState, spotifyConnected);

  let hint = "select next cell";
  if (!board || !board.cells?.length) {
    hint = "no board";
  } else if (!spotifyConnected) {
    hint = "connect spotify first";
  } else if (!selectable) {
    hint = "waiting";
  }

  return (
    <section className="flex h-full flex-col border-r border-edge bg-panel2">
      <PanelHead title="Queuing" hint={hint} />
      <div className="flex-1 overflow-auto p-3">
        {!board || !board.cells?.length ? (
          <Empty>No board loaded. Go to Board Builder → "Load into Game".</Empty>
        ) : (
          <BoardGrid board={board} selectable={selectable} onSelect={onSelect} />
        )}
      </div>
    </section>
  );
}

function BoardGrid({ board, selectable, onSelect }: { board: BoardData; selectable: boolean; onSelect: (r: number, c: number) => void }) {
  const byKey = new Map<string, BoardCell>();
  for (const c of board.cells ?? []) byKey.set(`${c.row},${c.col}`, c);

  const categories: string[] = [];
  for (let col = 1; col <= board.cols; col++) {
    let cat = "";
    for (let row = 1; row <= board.rows; row++) {
      const cell = byKey.get(`${row},${col}`);
      if (cell?.category) {
        cat = cell.category;
        break;
      }
    }
    categories.push(cat);
  }

  return (
    <div
      className="grid gap-1.5"
      style={{ gridTemplateColumns: `repeat(${board.cols}, minmax(0, 1fr))` }}
    >
      {categories.map((cat, i) => (
        <div
          key={`cat-${i}`}
          className="truncate rounded bg-panel3 px-1 py-1.5 text-center text-[10px] font-semibold uppercase tracking-wide text-slate-300"
          title={cat}
        >
          {cat || "—"}
        </div>
      ))}

      {Array.from({ length: board.rows }, (_, ri) => ri + 1).flatMap((row) =>
        Array.from({ length: board.cols }, (_, ci) => ci + 1).map((col) => {
          const cell = byKey.get(`${row},${col}`);
          const exhausted = !cell || cell.exhausted || cell.tracksLeft <= 0;
          const disabled = exhausted || !selectable;
          return (
            <button
              key={`${row},${col}`}
              disabled={disabled}
              onClick={() => onSelect(row, col)}
              title={
                cell
                  ? `${cell.category} · ${cell.points} pts · ${cell.tracksLeft} left`
                  : "empty"
              }
              className={[
                "flex h-9 items-center justify-center gap-1 rounded border text-center transition",
                disabled
                  ? "cursor-not-allowed border-edge/50 bg-panel/40 text-slate-700"
                  : "border-edge bg-panel text-accent hover:border-accent hover:bg-accent/10 active:scale-[0.97]",
              ].join(" ")}
            >
              <span className="text-xs font-bold leading-none">
                {cell ? cell.points : "—"}
              </span>
              {cell && !exhausted && (
                <span className="text-[10px] font-medium text-slate-500">
                  ×{cell.tracksLeft}
                </span>
              )}
              {exhausted && cell && (
                <span className="text-[8px] uppercase text-slate-600">done</span>
              )}
            </button>
          );
        }),
      )}
    </div>
  );
}

export function PanelHead({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="flex items-baseline justify-between border-b border-edge px-3 py-2">
      <h2 className="text-sm font-bold uppercase tracking-wide text-white">{title}</h2>
      {hint && <span className="text-[10px] uppercase tracking-wide text-slate-500">{hint}</span>}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center text-sm text-slate-600">
      {children}
    </div>
  );
}

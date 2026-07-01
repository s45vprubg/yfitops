import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import type { BoardCell, BoardData, GameState } from "@shared/protocol";
import { createAdminApi, type BoardSummary } from "../useAdminApi";

interface Props {
  board?: BoardData;
  gameState?: GameState;
  spotifyConnected: boolean;
  adminSecret: string;
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

function isGameActive(s?: GameState): boolean {
  return !!s && s !== "LOBBY" && s !== "GAME_OVER";
}

export default function BoardPanel({ board, gameState, spotifyConnected, adminSecret, onSelect }: Props) {
  const selectable = canSelectCell(gameState, spotifyConnected);
  const api = useMemo(() => createAdminApi(adminSecret), [adminSecret]);

  const [boards, setBoards] = useState<BoardSummary[]>([]);
  const [loading, setLoading] = useState(false);

  const refreshBoards = useCallback(async () => {
    try {
      setBoards((await api.listBoards()) ?? []);
    } catch { setBoards([]); }
  }, [api]);
  useEffect(() => { refreshBoards(); }, [refreshBoards]);

  const handleLoadBoard = async (boardId: string) => {
    if (!boardId) return;
    setLoading(true);
    try {
      await api.attachBoard(boardId, "session");
    } catch { /* engine broadcast updates UI */ }
    setLoading(false);
  };

  return (
    <section className="flex h-full flex-col border-r border-edge bg-panel2">
      {/* Header carries the board loader — the board is chosen here, in the
          board column, rather than up in the top bar. */}
      <div className="flex items-center justify-between gap-2 border-b border-edge px-3 py-2">
        <h2 className="text-sm font-bold uppercase tracking-wide text-white">Board</h2>
        <select
          disabled={loading || boards.length === 0 || isGameActive(gameState)}
          onChange={(e) => handleLoadBoard(e.target.value)}
          defaultValue=""
          title={isGameActive(gameState) ? "End the game to load a different board" : "Load a board into the game"}
          className="max-w-[10rem] rounded border border-edge bg-panel px-2 py-1 text-xs text-slate-200 outline-none focus:border-accent disabled:opacity-40"
        >
          <option value="" disabled>Load board…</option>
          {boards.map((b) => (
            <option key={b.id} value={b.id}>{b.name}</option>
          ))}
        </select>
      </div>
      <div className="flex-1 overflow-auto p-3">
        {!board || !board.cells?.length ? (
          <Empty>No board loaded. Pick one above, or build one in Board Builder.</Empty>
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
      className="grid gap-1"
      style={{ gridTemplateColumns: `repeat(${board.cols}, minmax(0, 1fr))` }}
    >
      {/* Category header row — the Jeopardy column titles. */}
      {categories.map((cat, i) => (
        <div
          key={`cat-${i}`}
          className="flex h-12 items-center justify-center rounded-sm bg-accent/15 px-1 text-center text-[10px] font-bold uppercase leading-tight tracking-wide text-accent"
          title={cat}
        >
          <span className="line-clamp-2">{cat || "—"}</span>
        </div>
      ))}

      {/* Point tiles: one column of descending values per category. */}
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
                "relative flex h-16 items-center justify-center rounded-sm border transition",
                exhausted
                  ? "border-edge/40 bg-panel/30 text-slate-700"
                  : disabled
                    ? "border-edge/60 bg-panel/60 text-amber-300/60"
                    : "border-edge bg-panel text-amber-300 hover:border-accent hover:bg-accent/10 active:scale-[0.97]",
              ].join(" ")}
            >
              {exhausted ? (
                <span className="text-[9px] uppercase tracking-wide text-slate-600">done</span>
              ) : (
                <>
                  <span className="text-lg font-extrabold leading-none">{cell ? cell.points : "—"}</span>
                  {cell && (
                    <span className="absolute bottom-0.5 right-1 text-[9px] font-medium text-slate-500">
                      ×{cell.tracksLeft}
                    </span>
                  )}
                </>
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

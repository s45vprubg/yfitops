import { useEffect, useRef, useState } from "react";
import type { BoardData } from "@shared/protocol";

interface Props {
  // THIS DEVICE'S OWN CONNECTION ONLY — category/points/exhausted per cell,
  // no track metadata (§4A-safe). Null while waiting for it to arrive.
  board: BoardData | null;
  onSelect: (row: number, col: number) => void;
}

// s8uim-01 recovery timer, mirroring DailyDoubleContestantScreen's
// DECISION_TIMEOUT_MS pattern exactly (same 8s window, same "resolve the
// ambiguity in favor of availability" reasoning): pick() sets pickedRef
// synchronously BEFORE the server has confirmed anything, same as that
// screen's decide(). Unlike that screen, nothing here was ever clearing the
// lock if confirmation never arrived — a connection that dies in the sliver
// between the optimistic lock and the `player.selectCell` write actually
// landing (see verdict s8uim-01) orphans the write with no retry, which
// disables this grid forever AND stalls the server's karaoke AND-gate for
// every other player, since the server never records a pendingPick. Note
// confirmation can never arrive while this component is still mounted: the
// parent (App.tsx) only renders PickCellScreen while pickerStatus.hasPicked
// is false, so the moment the server confirms, the parent swaps this screen
// out on its own. That means "this component is still mounted N ms after a
// tap" is itself sufficient proof confirmation hasn't landed — no need to
// thread pickerStatus in as a prop just to check it here.
const PICK_TIMEOUT_MS = 8000;

// The designated picker's own device — whoever most recently scored can
// choose the next board cell here instead of the admin always picking. A
// synchronous ref guard (same shape as DailyDoubleContestantScreen's
// decide()) stops a rapid double-tap from picking two different cells;
// the server is also idempotent (onPlayerSelectCell ignores a second pick
// once one is already recorded), so this is belt-and-suspenders.
export function PickCellScreen({ board, onSelect }: Props) {
  const [picked, setPicked] = useState<string | null>(null);
  const pickedRef = useRef<string | null>(null);

  useEffect(() => {
    if (picked == null) return;
    const timer = setTimeout(() => {
      pickedRef.current = null;
      setPicked(null);
    }, PICK_TIMEOUT_MS);
    return () => clearTimeout(timer);
  }, [picked]);

  if (!board) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center animate-fadeIn">
        <div className="text-sm uppercase tracking-[0.3em] text-yellow-400">your pick</div>
        <div className="text-2xl font-semibold text-neutral-200">Loading the board…</div>
      </div>
    );
  }

  const pick = (row: number, col: number) => {
    const key = `${row}:${col}`;
    if (pickedRef.current) return;
    pickedRef.current = key;
    setPicked(key);
    onSelect(row, col);
  };

  // Group by column so each category renders as one labeled row of cells,
  // same shape as the admin/stage grid (just not visually identical to it).
  const byCol = new Map<number, BoardData["cells"]>();
  for (const cell of board.cells) {
    const list = byCol.get(cell.col) ?? [];
    list.push(cell);
    byCol.set(cell.col, list);
  }
  const cols = [...byCol.keys()].sort((a, b) => a - b);

  return (
    <div className="flex flex-1 flex-col items-center gap-4 overflow-y-auto px-4 py-6 animate-fadeIn">
      <div className="text-center">
        <div className="text-sm uppercase tracking-[0.3em] text-yellow-400">it's your pick!</div>
        <div className="mt-1 text-sm text-neutral-500">Choose the next cell.</div>
      </div>
      <div className="flex w-full max-w-sm flex-col gap-3">
        {cols.map((col) => {
          const cellsInCol = (byCol.get(col) ?? []).slice().sort((a, b) => a.row - b.row);
          const category = cellsInCol[0]?.category ?? "";
          return (
            <div key={col} className="flex flex-col gap-1.5">
              <div className="text-xs font-semibold uppercase tracking-wide text-neutral-400">
                {category}
              </div>
              <div className="flex gap-1.5">
                {cellsInCol.map((cell) => {
                  const key = `${cell.row}:${cell.col}`;
                  const active = picked === key;
                  return (
                    <button
                      key={key}
                      onPointerDown={(e) => {
                        e.preventDefault();
                        if (!cell.exhausted) pick(cell.row, cell.col);
                      }}
                      disabled={cell.exhausted || pickedRef.current != null}
                      className={`flex-1 rounded-lg border px-2 py-3 text-center font-bold transition active:scale-[0.98] disabled:opacity-30 ${
                        active
                          ? "border-guess bg-guess/10 text-guess"
                          : "border-neutral-700 bg-panel text-neutral-200"
                      }`}
                    >
                      {cell.exhausted ? "—" : cell.points}
                    </button>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

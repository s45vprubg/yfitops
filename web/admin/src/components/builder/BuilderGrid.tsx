import { Droppable } from "@hello-pangea/dnd";
import type { LayoutCell, TrackData } from "../../useAdminApi";
import TrackCard from "./TrackCard";

interface Props {
  cols: number;
  cells: LayoutCell[];
  onRenameCategory: (col: number, name: string) => void;
  onRemoveColumn: (col: number) => void;
  onAddColumn: () => void;
}

export default function BuilderGrid({ cols, cells, onRenameCategory, onRemoveColumn, onAddColumn }: Props) {
  const getCellTracks = (row: number, col: number): TrackData[] => {
    const cell = cells.find((c) => c.row === row && c.col === col);
    return cell?.tracks ?? [];
  };

  const getCategory = (col: number): string => {
    const cell = cells.find((c) => c.col === col);
    return cell?.category ?? "";
  };

  const rowLabels = ["Easy", "Medium", "Hard", "Expert", "Master"];

  return (
    <div className="flex h-full flex-col overflow-auto p-3">
      <div className="mb-2 flex items-center gap-2">
        <h3 className="text-sm font-semibold text-slate-300">Board Grid</h3>
        {cols < 8 && (
          <button
            onClick={onAddColumn}
            className="rounded bg-accent/20 px-2 py-0.5 text-xs text-accent hover:bg-accent/30"
          >
            + Add Column
          </button>
        )}
      </div>

      <div
        className="grid gap-2"
        style={{
          gridTemplateColumns: `80px repeat(${cols}, minmax(140px, 1fr))`,
        }}
      >
        {/* Header row: row label spacer + category headers */}
        <div />
        {Array.from({ length: cols }, (_, ci) => {
          const col = ci + 1;
          const category = getCategory(col);
          return (
            <div key={`header-${col}`} className="flex flex-col gap-1">
              <input
                type="text"
                value={category}
                onChange={(e) => onRenameCategory(col, e.target.value)}
                placeholder="Category name"
                className="w-full rounded border border-edge bg-panel px-2 py-1 text-center text-xs font-semibold text-slate-200 placeholder-slate-600 outline-none focus:border-accent"
              />
              <button
                onClick={() => onRemoveColumn(col)}
                className="text-[10px] text-red-500 hover:text-red-400"
              >
                Remove
              </button>
            </div>
          );
        })}

        {/* Grid rows */}
        {Array.from({ length: 5 }, (_, ri) => {
          const row = ri + 1;
          return (
            <div key={`row-${row}`} className="contents">
              <div className="flex items-center justify-center text-xs text-slate-500">
                {rowLabels[ri]}
                <span className="ml-1 text-[10px] text-slate-600">
                  ({[100, 125, 150, 175, 200][ri]}pt)
                </span>
              </div>
              {Array.from({ length: cols }, (_, ci) => {
                const col = ci + 1;
                const droppableId = `cell-${row}-${col}`;
                const cellTracks = getCellTracks(row, col);
                return (
                  <Droppable key={droppableId} droppableId={droppableId}>
                    {(provided, snapshot) => (
                      <div
                        ref={provided.innerRef}
                        {...provided.droppableProps}
                        className={`min-h-[80px] rounded border p-1 ${
                          snapshot.isDraggingOver
                            ? "border-accent bg-accent/5"
                            : "border-edge bg-panel2"
                        }`}
                      >
                        <div className="flex flex-col gap-1">
                          {cellTracks.map((t, idx) => (
                            <TrackCard key={t.id} track={t} index={idx} />
                          ))}
                        </div>
                        {provided.placeholder}
                        {cellTracks.length === 0 && !snapshot.isDraggingOver && (
                          <div className="flex h-full items-center justify-center text-[10px] text-slate-600">
                            Drop tracks
                          </div>
                        )}
                      </div>
                    )}
                  </Droppable>
                );
              })}
            </div>
          );
        })}
      </div>
    </div>
  );
}

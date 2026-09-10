import { useEffect, useRef, useState } from "react";
import type { AdminApi, BoardSummary } from "../../useAdminApi";
import { useModal } from "../Modal";

interface Props {
  api: AdminApi;
  boards: BoardSummary[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  onRefresh: () => void;
}

export default function BoardSelector({ api, boards, selectedId, onSelect, onRefresh }: Props) {
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");

  const [error, setError] = useState<string | null>(null);
  const { confirm } = useModal();

  const selectedBoard = boards.find((b) => b.id === selectedId);
  const [ddCount, setDdCount] = useState(0);
  const ddCountTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Re-sync the input whenever the selected board (or its stored count) changes.
  useEffect(() => { setDdCount(selectedBoard?.dailyDoubleCount ?? 0); }, [selectedBoard?.id, selectedBoard?.dailyDoubleCount]);

  const handleDailyDoubleCountChange = (n: number) => {
    if (!selectedId) return;
    setDdCount(n);
    if (ddCountTimeout.current !== null) clearTimeout(ddCountTimeout.current);
    ddCountTimeout.current = setTimeout(() => {
      api.setDailyDoubleCount(selectedId, n).then(onRefresh).catch(() => {});
    }, 500);
  };

  const handleCreate = async () => {
    if (!newName.trim()) return;
    setCreating(true);
    setError(null);
    try {
      const board = await api.createBoard(newName.trim());
      setNewName("");
      onRefresh();
      onSelect(board.id);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to create board");
    } finally {
      setCreating(false);
    }
  };

  const handleDelete = async () => {
    if (!selectedId) return;
    if (
      !(await confirm({
        title: "Delete board?",
        body: "Delete this board and all its tracks? This cannot be undone.",
        confirmLabel: "Delete",
        danger: true,
      }))
    )
      return;
    try {
      await api.deleteBoard(selectedId);
      onRefresh();
      onSelect("");
    } catch {
      // handle error
    }
  };

  return (
    <div className="flex items-center gap-2 border-b border-edge bg-panel p-2">
      <select
        value={selectedId ?? ""}
        onChange={(e) => onSelect(e.target.value)}
        className="rounded border border-edge bg-panel2 px-2 py-1 text-sm text-slate-200"
      >
        <option value="">-- Select a board --</option>
        {boards.map((b) => (
          <option key={b.id} value={b.id}>
            {b.name} ({b.cols} col{b.cols !== 1 ? "s" : ""})
          </option>
        ))}
      </select>

      <div className="flex items-center gap-1">
        <input
          type="text"
          placeholder="New board name"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && handleCreate()}
          className="rounded border border-edge bg-panel px-2 py-1 text-sm text-slate-200 placeholder-slate-500 outline-none focus:border-accent"
        />
        <button
          onClick={handleCreate}
          disabled={creating || !newName.trim()}
          className="rounded bg-accent/20 px-2 py-1 text-sm text-accent hover:bg-accent/30 disabled:opacity-50"
        >
          + Create
        </button>
      </div>

      {selectedId && selectedBoard && (
        <label className="ml-auto flex items-center gap-1.5 text-xs text-slate-400">
          Daily doubles
          <input
            type="number"
            min={0}
            max={5 * Math.max(selectedBoard.cols, 1)}
            value={ddCount}
            onChange={(e) => handleDailyDoubleCountChange(Math.max(0, Number(e.target.value)))}
            className="w-14 rounded border border-edge bg-panel2 px-1.5 py-1 text-right text-sm text-slate-200 outline-none focus:border-accent"
            title="Number of Daily Double cells randomly assigned each Start Game"
          />
        </label>
      )}

      {selectedId && (
        <button
          onClick={handleDelete}
          className={`rounded bg-red-900/40 px-2 py-1 text-sm text-red-400 hover:bg-red-900/60 ${selectedBoard ? "" : "ml-auto"}`}
        >
          Delete Board
        </button>
      )}
      {error && (
        <span className="text-xs text-red-400">{error}</span>
      )}
    </div>
  );
}

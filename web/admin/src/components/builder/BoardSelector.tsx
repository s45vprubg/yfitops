import { useState } from "react";
import type { AdminApi, BoardSummary } from "../../useAdminApi";

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
    if (!confirm("Delete this board and all its tracks?")) return;
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

      {selectedId && (
        <button
          onClick={handleDelete}
          className="ml-auto rounded bg-red-900/40 px-2 py-1 text-sm text-red-400 hover:bg-red-900/60"
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

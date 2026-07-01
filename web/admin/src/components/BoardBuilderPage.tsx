import { useState, useEffect, useCallback, useMemo, useRef } from "react";
import { DragDropContext, type DropResult } from "@hello-pangea/dnd";
import { createAdminApi, type BoardSummary, type TrackData, type LayoutCell } from "../useAdminApi";
import BoardSelector from "./builder/BoardSelector";
import HoldingArea from "./builder/HoldingArea";
import BuilderGrid from "./builder/BuilderGrid";
import { useModal } from "./Modal";

interface Props {
  secret: string;
}

export default function BoardBuilderPage({ secret }: Props) {
  const api = useMemo(() => createAdminApi(secret), [secret]);
  const { confirm, promptText } = useModal();

  const [boards, setBoards] = useState<BoardSummary[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [unplaced, setUnplaced] = useState<TrackData[]>([]);
  const [cells, setCells] = useState<LayoutCell[]>([]);
  const [cols, setCols] = useState(0);

  const loadBoards = useCallback(async () => {
    try {
      const list = await api.listBoards();
      setBoards(list ?? []);
    } catch {
      setBoards([]);
    }
  }, [api]);

  const loadBoardData = useCallback(async (boardId: string) => {
    try {
      const [up, layout] = await Promise.all([
        api.unplacedTracks(boardId),
        api.getLayout(boardId),
      ]);
      setUnplaced(up ?? []);
      setCells(layout?.cells ?? []);
      setCols(layout?.cols ?? 0);
    } catch {
      setUnplaced([]);
      setCells([]);
      setCols(0);
    }
  }, [api]);

  useEffect(() => { loadBoards(); }, [loadBoards]);

  useEffect(() => {
    if (selectedId) loadBoardData(selectedId);
    else { setUnplaced([]); setCells([]); setCols(0); }
  }, [selectedId, loadBoardData]);

  const refresh = () => { if (selectedId) loadBoardData(selectedId); };

  const handleDeleteTrack = async (trackId: string) => {
    if (!selectedId) return;
    await api.deleteTrack(selectedId, trackId);
    refresh();
  };

  const handleAddColumn = async () => {
    if (!selectedId) return;
    const name = await promptText({ title: "New category", placeholder: "Category name", confirmLabel: "Add" });
    if (!name?.trim()) return;
    await api.addColumn(selectedId, name.trim());
    refresh();
  };

  const handleRemoveColumn = async (col: number) => {
    if (!selectedId) return;
    if (
      !(await confirm({
        title: "Remove column?",
        body: "Tracks will return to the holding area.",
        confirmLabel: "Remove",
        danger: true,
      }))
    )
      return;
    await api.removeColumn(selectedId, col);
    refresh();
  };

  const renameCategoryTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  const handleRenameCategory = (col: number, name: string) => {
    if (!selectedId) return;
    setCells((prev) =>
      prev.map((c) => (c.col === col ? { ...c, category: name } : c))
    );
    if (renameCategoryTimeout.current !== null) clearTimeout(renameCategoryTimeout.current);
    renameCategoryTimeout.current = setTimeout(() => {
      api.renameCategory(selectedId, col, name);
    }, 500);
  };

  const handleDragEnd = async (result: DropResult) => {
    if (!selectedId || !result.destination) return;

    const src = result.source;
    const dst = result.destination;
    const trackId = result.draggableId;

    const parseCell = (id: string) => {
      const m = id.match(/^cell-(\d+)-(\d+)$/);
      return m ? { row: parseInt(m[1]), col: parseInt(m[2]) } : null;
    };

    const srcCell = parseCell(src.droppableId);
    const dstCell = parseCell(dst.droppableId);

    if (src.droppableId === "holding" && dstCell) {
      await api.placeTrack(selectedId, dstCell.row, dstCell.col, trackId, dst.index);
    } else if (srcCell && dst.droppableId === "holding") {
      await api.unplaceTrack(selectedId, srcCell.row, srcCell.col, trackId);
    } else if (srcCell && dstCell) {
      await api.unplaceTrack(selectedId, srcCell.row, srcCell.col, trackId);
      await api.placeTrack(selectedId, dstCell.row, dstCell.col, trackId, dst.index);
    }

    refresh();
  };

  if (!selectedId) {
    return (
      <div className="flex h-full flex-col bg-[#05070a]">
        <BoardSelector
          api={api}
          boards={boards}
          selectedId={selectedId}
          onSelect={setSelectedId}
          onRefresh={loadBoards}
        />
        <div className="flex flex-1 items-center justify-center text-slate-500">
          Select or create a board to start building.
        </div>
      </div>
    );
  }

  return (
    <DragDropContext onDragEnd={handleDragEnd}>
      <div className="flex h-full flex-col bg-[#05070a]">
        <BoardSelector
          api={api}
          boards={boards}
          selectedId={selectedId}
          onSelect={setSelectedId}
          onRefresh={loadBoards}
        />
        <div className="grid min-h-0 flex-1 grid-cols-[320px_1fr]">
          <HoldingArea
            api={api}
            boardId={selectedId}
            tracks={unplaced}
            onRefresh={refresh}
            onDeleteTrack={handleDeleteTrack}
          />
          <BuilderGrid
            cols={cols}
            cells={cells}
            onRenameCategory={handleRenameCategory}
            onRemoveColumn={handleRemoveColumn}
            onAddColumn={handleAddColumn}
          />
        </div>
      </div>
    </DragDropContext>
  );
}

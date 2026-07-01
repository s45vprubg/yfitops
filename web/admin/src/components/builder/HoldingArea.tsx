import { useState, useMemo } from "react";
import { Droppable } from "@hello-pangea/dnd";
import type { AdminApi, TrackData } from "../../useAdminApi";
import TrackCard from "./TrackCard";
import SpotifySearch from "./SpotifySearch";
import ImportPlaylist from "./ImportPlaylist";
import { useModal } from "../Modal";

type SortKey = "none" | "title" | "artist";

interface Props {
  api: AdminApi;
  boardId: string;
  tracks: TrackData[];
  onRefresh: () => void;
  onDeleteTrack: (trackId: string) => void;
  onToggleOverride: (trackId: string, override: boolean) => void;
}

export default function HoldingArea({ api, boardId, tracks, onRefresh, onDeleteTrack, onToggleOverride }: Props) {
  const [sortBy, setSortBy] = useState<SortKey>("none");
  const [rescanning, setRescanning] = useState(false);
  const [building, setBuilding] = useState(false);
  const [aiError, setAiError] = useState<string | null>(null);
  const { confirm } = useModal();

  const noLyrics = tracks.filter((t) => t.hasSyncedLyrics === false && !t.lyricsOverride).length;

  const handleRescan = async () => {
    setRescanning(true);
    try {
      await api.rescanLyrics(boardId);
      onRefresh();
    } catch { /* surfaced via list refresh */ }
    setRescanning(false);
  };

  const handleAiBuild = async () => {
    const ok = await confirm({
      title: "Build board with AI?",
      body: "The AI will group your tracks into ~6 themed categories and lay out the board. This REPLACES the current layout (tracks return to the library first).",
      confirmLabel: "Build",
    });
    if (!ok) return;
    setBuilding(true);
    setAiError(null);
    try {
      await api.aiBuild(boardId);
      onRefresh();
    } catch (e) {
      setAiError(e instanceof Error ? e.message : "AI build failed");
    }
    setBuilding(false);
  };

  const sorted = useMemo(() => {
    if (sortBy === "none") return tracks;
    const key = sortBy === "title" ? "song" : "artist";
    return [...tracks].sort((a, b) => (a[key] ?? "").localeCompare(b[key] ?? ""));
  }, [tracks, sortBy]);

  return (
    <div className="flex h-full flex-col gap-3 overflow-hidden border-r border-edge p-3">
      <h3 className="text-sm font-semibold text-slate-300">Track Library</h3>

      <SpotifySearch api={api} boardId={boardId} onTrackAdded={onRefresh} />
      <ImportPlaylist api={api} boardId={boardId} onImported={onRefresh} />

      {/* AI board builder: auto-categorize + lay out the whole board. */}
      <button
        onClick={handleAiBuild}
        disabled={building || tracks.length === 0}
        className="rounded border border-accent/60 bg-accent/10 px-2 py-1.5 text-xs font-bold uppercase tracking-wide text-accent hover:bg-accent/20 disabled:opacity-40"
        title="Group tracks into themed categories and lay out the board with AI"
      >
        {building ? "Building…" : "✨ Build with AI"}
      </button>
      {aiError && <div className="text-[11px] text-red-400">{aiError}</div>}

      <div className="flex items-center justify-between rounded border border-edge bg-panel2 px-2 py-1 text-[11px]">
        <span className={noLyrics > 0 ? "text-amber-400" : "text-slate-500"}>
          {noLyrics > 0 ? `⚠ ${noLyrics} without lyrics` : "✓ all tracks have lyrics"}
        </span>
        <button
          onClick={handleRescan}
          disabled={rescanning}
          className="rounded border border-edge px-1.5 py-0.5 font-semibold text-slate-300 hover:text-white disabled:opacity-40"
          title="Re-check LRCLIB for synced lyrics"
        >
          {rescanning ? "checking…" : "Check lyrics"}
        </button>
      </div>

      <div className="flex items-center justify-between">
        <span className="text-xs text-slate-500">
          {tracks.length} unplaced {tracks.length === 1 ? "track" : "tracks"}
        </span>
        <div className="flex gap-1">
          {(["none", "title", "artist"] as SortKey[]).map((key) => (
            <button
              key={key}
              onClick={() => setSortBy(key)}
              className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${
                sortBy === key
                  ? "bg-accent/20 text-accent"
                  : "text-slate-500 hover:text-slate-300"
              }`}
            >
              {key === "none" ? "Default" : key === "title" ? "Title" : "Artist"}
            </button>
          ))}
        </div>
      </div>

      <Droppable droppableId="holding">
        {(provided) => (
          <div
            ref={provided.innerRef}
            {...provided.droppableProps}
            className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto"
          >
            {sorted.map((t, i) => (
              <TrackCard key={t.id} track={t} index={i} onDelete={onDeleteTrack} onToggleOverride={onToggleOverride} />
            ))}
            {provided.placeholder}
          </div>
        )}
      </Droppable>
    </div>
  );
}

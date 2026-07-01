import { useState, useMemo } from "react";
import { Droppable } from "@hello-pangea/dnd";
import type { AdminApi, TrackData } from "../../useAdminApi";
import TrackCard from "./TrackCard";
import SpotifySearch from "./SpotifySearch";
import ImportPlaylist from "./ImportPlaylist";

type SortKey = "none" | "title" | "artist";

interface Props {
  api: AdminApi;
  boardId: string;
  tracks: TrackData[];
  onRefresh: () => void;
  onDeleteTrack: (trackId: string) => void;
}

export default function HoldingArea({ api, boardId, tracks, onRefresh, onDeleteTrack }: Props) {
  const [sortBy, setSortBy] = useState<SortKey>("none");

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
              <TrackCard key={t.id} track={t} index={i} onDelete={onDeleteTrack} />
            ))}
            {provided.placeholder}
          </div>
        )}
      </Droppable>
    </div>
  );
}

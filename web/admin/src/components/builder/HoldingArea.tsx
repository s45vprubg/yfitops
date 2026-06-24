import { Droppable } from "@hello-pangea/dnd";
import type { AdminApi, TrackData } from "../../useAdminApi";
import TrackCard from "./TrackCard";
import SpotifySearch from "./SpotifySearch";
import ImportPlaylist from "./ImportPlaylist";

interface Props {
  api: AdminApi;
  boardId: string;
  tracks: TrackData[];
  onRefresh: () => void;
  onDeleteTrack: (trackId: string) => void;
}

export default function HoldingArea({ api, boardId, tracks, onRefresh, onDeleteTrack }: Props) {
  return (
    <div className="flex h-full flex-col gap-3 overflow-hidden border-r border-edge p-3">
      <h3 className="text-sm font-semibold text-slate-300">Track Library</h3>

      <SpotifySearch api={api} boardId={boardId} onTrackAdded={onRefresh} />
      <ImportPlaylist api={api} boardId={boardId} onImported={onRefresh} />

      <div className="text-xs text-slate-500">
        {tracks.length} unplaced {tracks.length === 1 ? "track" : "tracks"}
      </div>

      <Droppable droppableId="holding">
        {(provided) => (
          <div
            ref={provided.innerRef}
            {...provided.droppableProps}
            className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto"
          >
            {tracks.map((t, i) => (
              <TrackCard key={t.id} track={t} index={i} onDelete={onDeleteTrack} />
            ))}
            {provided.placeholder}
          </div>
        )}
      </Droppable>
    </div>
  );
}

import { Draggable } from "@hello-pangea/dnd";
import type { TrackData } from "../../useAdminApi";

interface Props {
  track: TrackData;
  index: number;
  onDelete?: (trackId: string) => void;
}

export default function TrackCard({ track, index, onDelete }: Props) {
  return (
    <Draggable draggableId={track.id} index={index}>
      {(provided, snapshot) => (
        <div
          ref={provided.innerRef}
          {...provided.draggableProps}
          {...provided.dragHandleProps}
          className={`flex items-center gap-2 rounded border px-2 py-1 text-xs ${
            snapshot.isDragging
              ? "border-accent bg-accent/10"
              : "border-edge bg-panel2"
          }`}
        >
          {track.albumArt && (
            <img src={track.albumArt} alt="" className="h-8 w-8 rounded" />
          )}
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium text-slate-100">{track.song}</div>
            <div className="truncate text-slate-400">{track.artist}</div>
          </div>
          {onDelete && (
            <button
              onClick={(e) => { e.stopPropagation(); onDelete(track.id); }}
              className="shrink-0 text-red-400 hover:text-red-300"
              title="Remove from library"
            >
              &times;
            </button>
          )}
        </div>
      )}
    </Draggable>
  );
}

import { Draggable } from "@hello-pangea/dnd";
import type { TrackData } from "../../useAdminApi";

interface Props {
  track: TrackData;
  index: number;
  onDelete?: (trackId: string) => void;
  onToggleOverride?: (trackId: string, override: boolean) => void;
}

// A track is greyed out (won't be auto-played) when LRCLIB has no synced lyrics
// AND the admin hasn't overridden it. hasSyncedLyrics === null means not yet
// probed (treated as playable until a re-scan).
function isLyricLess(t: TrackData): boolean {
  return t.hasSyncedLyrics === false;
}

export default function TrackCard({ track, index, onDelete, onToggleOverride }: Props) {
  const lyricLess = isLyricLess(track);
  const dimmed = lyricLess && !track.lyricsOverride;

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
          } ${dimmed ? "opacity-45" : ""}`}
          title={lyricLess ? "No synced lyrics — won't play unless overridden" : undefined}
        >
          {track.albumArt && (
            <img src={track.albumArt} alt="" className={`h-8 w-8 rounded ${dimmed ? "grayscale" : ""}`} />
          )}
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium text-slate-100">{track.song}</div>
            <div className="truncate text-slate-400">{track.artist}</div>
          </div>

          {lyricLess && (
            <button
              onClick={(e) => { e.stopPropagation(); onToggleOverride?.(track.id, !track.lyricsOverride); }}
              className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] font-bold uppercase ${
                track.lyricsOverride
                  ? "bg-amber-500/20 text-amber-300"
                  : "border border-amber-700/60 text-amber-400/80 hover:bg-amber-950/40"
              }`}
              title={
                track.lyricsOverride
                  ? "Playing despite no lyrics — click to disable"
                  : "No synced lyrics. Click to allow playing anyway (no karaoke)."
              }
            >
              {track.lyricsOverride ? "♪ playing" : "⚠ no lyrics"}
            </button>
          )}

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

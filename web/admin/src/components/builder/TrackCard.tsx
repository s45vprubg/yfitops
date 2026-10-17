import { Draggable } from "@hello-pangea/dnd";
import type { LyricsStatus } from "../../useAdminApi";

// Generalized to any track carrying the lyrics-flagging fields — used by
// both Board Builder (TrackData, which has a real admin override) and Solo
// Karaoke (SoloKaraokeTrack, which has no override concept since its lyrics
// gating is purely informational/soft — see solokaraoke_types.go). Structural
// typing means both existing concrete types satisfy this without changes.
export interface LyricsFlaggedTrack {
  id: string;
  song: string;
  artist: string;
  albumArt: string;
  lyricsStatus: LyricsStatus;
  lyricsOverride?: boolean;
}

// Generic over the concrete track type (rather than widening every caller's
// callback to accept a bare LyricsFlaggedTrack) so a Board Builder call site
// still gets a TrackData back in onOpenManualLyrics, and a Solo Karaoke call
// site gets a SoloKaraokeTrack back — avoids a strictFunctionTypes
// contravariance error on the callback props that a single non-generic
// LyricsFlaggedTrack parameter type would otherwise cause.
interface Props<T extends LyricsFlaggedTrack> {
  track: T;
  index: number;
  onDelete?: (trackId: string) => void;
  onToggleOverride?: (trackId: string, override: boolean) => void;
  onRequeryLyrics?: (trackId: string) => void;
  onOpenManualLyrics?: (track: T) => void;
  requeryingLyrics?: boolean;
}

// A track is greyed out (won't auto-play) when its song has no resolved
// lyrics AND the admin hasn't overridden it. lyricsStatus === "" means never
// attempted (treated as playable until a check is run); "pending"/"not_found"
// means a check WAS run and came back without usable lyrics.
function needsLyrics(t: LyricsFlaggedTrack): boolean {
  return t.lyricsStatus === "pending" || t.lyricsStatus === "not_found";
}

function statusLabel(t: LyricsFlaggedTrack): string {
  if (t.lyricsStatus === "pending") return "⏳ check failed";
  if (t.lyricsStatus === "not_found") return "⚠ no lyrics";
  return "";
}

function statusTitle(t: LyricsFlaggedTrack): string | undefined {
  if (t.lyricsStatus === "pending") return "The last lyrics check errored (timeout/rate limit) — use Requery to try again.";
  if (t.lyricsStatus === "not_found") return "No synced lyrics — won't play unless overridden";
  return undefined;
}

export default function TrackCard<T extends LyricsFlaggedTrack>({
  track,
  index,
  onDelete,
  onToggleOverride,
  onRequeryLyrics,
  onOpenManualLyrics,
  requeryingLyrics,
}: Props<T>) {
  const lyricLess = needsLyrics(track);
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
          title={statusTitle(track)}
        >
          {track.albumArt && (
            <img src={track.albumArt} alt="" className={`h-8 w-8 rounded ${dimmed ? "grayscale" : ""}`} />
          )}
          <div className="min-w-0 flex-1">
            <div className="truncate font-medium text-slate-100">{track.song}</div>
            <div className="truncate text-slate-400">{track.artist}</div>
          </div>

          {lyricLess && (
            <div className="flex shrink-0 items-center gap-1">
              {/* Override toggle only exists where the caller wires it
                  (Board Builder's TrackData has a real admin-override field).
                  Solo Karaoke's gating is soft with no override concept, so
                  this renders as a plain non-interactive status badge there. */}
              {onToggleOverride ? (
                <button
                  onClick={(e) => { e.stopPropagation(); onToggleOverride(track.id, !track.lyricsOverride); }}
                  className={`rounded px-1.5 py-0.5 text-[10px] font-bold uppercase ${
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
                  {track.lyricsOverride ? "♪ playing" : statusLabel(track)}
                </button>
              ) : (
                <span
                  className="rounded border border-amber-700/60 px-1.5 py-0.5 text-[10px] font-bold uppercase text-amber-400/80"
                  title={statusTitle(track)}
                >
                  {statusLabel(track)}
                </span>
              )}
              {onRequeryLyrics && (
                <button
                  onClick={(e) => { e.stopPropagation(); onRequeryLyrics(track.id); }}
                  disabled={requeryingLyrics}
                  className="rounded border border-edge px-1 py-0.5 text-[10px] text-slate-400 hover:text-white disabled:opacity-40"
                  title="Retry LRCLIB for this song"
                >
                  {requeryingLyrics ? "…" : "↻"}
                </button>
              )}
              {onOpenManualLyrics && (
                <button
                  onClick={(e) => { e.stopPropagation(); onOpenManualLyrics(track); }}
                  className="rounded border border-edge px-1 py-0.5 text-[10px] text-slate-400 hover:text-white"
                  title="Paste LRC lyrics text in by hand"
                >
                  ✎
                </button>
              )}
            </div>
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

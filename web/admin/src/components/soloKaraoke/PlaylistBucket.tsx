import { useCallback, useRef, useState } from "react";
import { Droppable } from "@hello-pangea/dnd";
import type { AdminApi, SoloKaraokeTrack, SpotifyResult } from "../../useAdminApi";
import TrackCard from "../builder/TrackCard";

interface Props {
  api: AdminApi;
  tracks: SoloKaraokeTrack[]; // already ordered by position
  onRefresh: () => void;
  onDeleteTrack: (trackId: string) => void;
  requeryingId: string | null;
  onRequeryLyrics: (trackId: string) => void;
  onOpenManualLyrics: (track: SoloKaraokeTrack) => void;
  nowPlayingTrackId?: string;
}

// PlaylistBucket — the ordered queue the engine actually plays from, adapted
// from builder/DailyDoubleBucket.tsx. Two differences from that bucket:
//  1. Lyrics gating is SOFT here (never blocks adding), unlike Daily
//     Double's hard 412 reject — so this also keeps the direct search-add
//     box DailyDoubleBucket has, just without a lyrics precondition.
//  2. This bucket is ORDERED (`position`), so a same-droppable drag-end
//     (reordering within the Playlist) is meaningful — SoloKaraokePage's
//     single top-level DragDropContext detects that case and calls
//     api.reorderSoloKaraokePlaylist with the new full ordered id list;
//     this component only renders the Draggable list, it doesn't decide
//     the move itself (there can only be one DragDropContext ancestor).
export default function PlaylistBucket({
  api, tracks, onRefresh, onDeleteTrack, requeryingId, onRequeryLyrics, onOpenManualLyrics, nowPlayingTrackId,
}: Props) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SpotifyResult[]>([]);
  const [loading, setLoading] = useState(false);
  const [adding, setAdding] = useState<Set<string>>(new Set());
  const [addError, setAddError] = useState<string | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const search = useCallback(
    (q: string) => {
      if (debounceRef.current !== null) clearTimeout(debounceRef.current);
      if (!q.trim()) {
        setResults([]);
        return;
      }
      debounceRef.current = setTimeout(async () => {
        setLoading(true);
        try {
          setResults(await api.searchSpotify(q, 10));
        } catch {
          setResults([]);
        } finally {
          setLoading(false);
        }
      }, 300);
    },
    [api],
  );

  const addTrack = async (r: SpotifyResult) => {
    setAdding((s) => new Set(s).add(r.uri));
    setAddError(null);
    try {
      await api.addSoloKaraokePlaylistTrack({
        spotifyUri: r.uri,
        artist: r.artist,
        song: r.song,
        albumArt: r.albumArt,
        durationMs: r.durationMs,
      });
      onRefresh();
    } catch (e) {
      setAddError(e instanceof Error ? e.message : "Failed to add to playlist");
    } finally {
      setAdding((s) => {
        const n = new Set(s);
        n.delete(r.uri);
        return n;
      });
    }
  };

  return (
    <div className="flex h-full flex-col gap-3 overflow-hidden p-3">
      <h3 className="text-sm font-semibold text-slate-300">Playlist</h3>
      <p className="text-[11px] text-slate-500">
        Drag from the Library, or search to add directly. Drag within this list to reorder.
      </p>

      <input
        type="text"
        placeholder="Search Spotify to add directly..."
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          search(e.target.value);
        }}
        className="rounded border border-edge bg-panel px-2 py-1 text-sm text-slate-200 placeholder-slate-500 outline-none focus:border-accent"
      />
      {loading && <div className="text-xs text-slate-500">Searching...</div>}
      {addError && <div className="text-[11px] text-red-400">{addError}</div>}
      {results.length > 0 && (
        <div className="max-h-40 overflow-y-auto rounded border border-edge bg-panel">
          {results.map((r) => (
            <div key={r.uri} className="flex items-center gap-2 border-b border-edge px-2 py-1 last:border-0">
              {r.albumArt && <img src={r.albumArt} alt="" className="h-8 w-8 rounded" />}
              <div className="min-w-0 flex-1">
                <div className="truncate text-xs font-medium text-slate-100">{r.song}</div>
                <div className="truncate text-xs text-slate-400">{r.artist}</div>
              </div>
              <button
                onClick={() => addTrack(r)}
                disabled={adding.has(r.uri)}
                className="shrink-0 rounded bg-accent/20 px-2 py-0.5 text-xs text-accent hover:bg-accent/30 disabled:opacity-50"
              >
                {adding.has(r.uri) ? "..." : "Add"}
              </button>
            </div>
          ))}
        </div>
      )}

      <div className="text-xs text-slate-500">
        {tracks.length} {tracks.length === 1 ? "song" : "songs"} queued
      </div>

      <Droppable droppableId="skPlaylist">
        {(provided, snapshot) => (
          <div
            ref={provided.innerRef}
            {...provided.droppableProps}
            className={`flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto rounded border border-dashed p-1 transition ${
              snapshot.isDraggingOver ? "border-accent bg-accent/5" : "border-transparent"
            }`}
          >
            {tracks.length === 0 && (
              <div className="rounded border border-edge/50 px-2 py-3 text-center text-[11px] text-slate-600">
                Drag a track here from the Library, or search above.
              </div>
            )}
            {tracks.map((t, idx) => (
              <div key={t.id} className={t.id === nowPlayingTrackId ? "rounded ring-1 ring-accent" : undefined}>
                <TrackCard
                  track={t}
                  index={idx}
                  onDelete={onDeleteTrack}
                  onRequeryLyrics={onRequeryLyrics}
                  onOpenManualLyrics={onOpenManualLyrics}
                  requeryingLyrics={requeryingId === t.id}
                />
              </div>
            ))}
            {provided.placeholder}
          </div>
        )}
      </Droppable>
    </div>
  );
}

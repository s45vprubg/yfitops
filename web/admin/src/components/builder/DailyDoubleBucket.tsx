import { useCallback, useRef, useState } from "react";
import { Droppable } from "@hello-pangea/dnd";
import type { AdminApi, DailyDoubleTrackData, SpotifyResult } from "../../useAdminApi";

interface Props {
  api: AdminApi;
  boardId: string;
  tracks: DailyDoubleTrackData[];
  onRefresh: () => void;
}

// DailyDoubleBucket — the standalone song pool a Daily Double draws its 5
// choices from (§7 sidenote), independent of the grid's category Track
// library. A valid drop target for tracks dragged from the Track Library or a
// grid cell (handled in BoardBuilderPage.handleDragEnd) — dropping here is a
// MOVE: the source track is deleted from board_tracks entirely (cascading out
// of any cell placement) and re-created here, so a Daily Double track can
// never appear on the Jeopardy board. Bucket tracks are not themselves
// draggable back out; remove via the ✕ button. Simpler than HoldingArea in
// other ways too: no lyric-availability gating, no AI build.
export default function DailyDoubleBucket({ api, boardId, tracks, onRefresh }: Props) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SpotifyResult[]>([]);
  const [loading, setLoading] = useState(false);
  const [adding, setAdding] = useState<Set<string>>(new Set());
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
    try {
      await api.addDailyDoubleTrack(boardId, {
        spotifyUri: r.uri,
        artist: r.artist,
        song: r.song,
        albumArt: r.albumArt,
        durationMs: r.durationMs,
      });
      onRefresh();
    } catch {
      // dedup or other error — silently skip
    } finally {
      setAdding((s) => {
        const n = new Set(s);
        n.delete(r.uri);
        return n;
      });
    }
  };

  const removeTrack = async (trackId: string) => {
    try {
      await api.deleteDailyDoubleTrack(boardId, trackId);
    } finally {
      onRefresh();
    }
  };

  return (
    <div className="flex h-full flex-col gap-3 overflow-hidden border-l border-edge p-3">
      <h3 className="text-sm font-semibold text-slate-300">Daily Double Bucket</h3>
      <p className="text-[11px] text-slate-500">
        Songs a Daily Double draws 5 choices from. Independent of the grid.
      </p>

      <input
        type="text"
        placeholder="Search Spotify to add..."
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          search(e.target.value);
        }}
        className="rounded border border-edge bg-panel px-2 py-1 text-sm text-slate-200 placeholder-slate-500 outline-none focus:border-accent"
      />
      {loading && <div className="text-xs text-slate-500">Searching...</div>}
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
        {tracks.length} song{tracks.length === 1 ? "" : "s"} in the bucket
        {tracks.length > 0 && tracks.length < 5 && (
          <span className="text-amber-400"> — fewer than 5, offers fewer choices</span>
        )}
      </div>

      <Droppable droppableId="dailyDouble">
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
                Drag a track here from the Track Library or a cell to move it into the bucket.
              </div>
            )}
            {tracks.map((t) => (
              <div key={t.id} className="flex items-center gap-2 rounded border border-edge bg-panel2 px-2 py-1">
                {t.albumArt && <img src={t.albumArt} alt="" className="h-8 w-8 rounded" />}
                <div className="min-w-0 flex-1">
                  <div className="truncate text-xs font-medium text-slate-100">{t.song}</div>
                  <div className="truncate text-xs text-slate-400">{t.artist}</div>
                </div>
                <button
                  onClick={() => removeTrack(t.id)}
                  className="shrink-0 rounded px-1.5 py-0.5 text-xs text-slate-500 hover:text-red-400"
                  title="Remove from bucket"
                >
                  ✕
                </button>
              </div>
            ))}
            {provided.placeholder}
          </div>
        )}
      </Droppable>
    </div>
  );
}

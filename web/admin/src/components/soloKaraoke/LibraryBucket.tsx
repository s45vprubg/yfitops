import { useRef, useState, useCallback } from "react";
import { Droppable } from "@hello-pangea/dnd";
import type { AdminApi, SoloKaraokeTrack, SpotifyResult } from "../../useAdminApi";
import TrackCard from "../builder/TrackCard";

interface Props {
  api: AdminApi;
  tracks: SoloKaraokeTrack[];
  onRefresh: () => void;
  onDeleteTrack: (trackId: string) => void;
  requeryingId: string | null;
  onRequeryLyrics: (trackId: string) => void;
  onOpenManualLyrics: (track: SoloKaraokeTrack) => void;
}

// LibraryBucket — Solo Karaoke's holding pool, adapted from
// builder/HoldingArea.tsx + SpotifySearch.tsx + ImportPlaylist.tsx. Unlike
// Board Builder's holding area, this isn't board-scoped and has no AI-build
// button (nothing to lay out — Solo Karaoke has no grid). Drag a track from
// here into the Playlist bucket to queue it; the drag/move itself is handled
// by SoloKaraokePage's single top-level DragDropContext.
export default function LibraryBucket({
  api, tracks, onRefresh, onDeleteTrack, requeryingId, onRequeryLyrics, onOpenManualLyrics,
}: Props) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SpotifyResult[]>([]);
  const [loading, setLoading] = useState(false);
  const [adding, setAdding] = useState<Set<string>>(new Set());
  const [searchError, setSearchError] = useState<string | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const [importUri, setImportUri] = useState("");
  const [importing, setImporting] = useState(false);
  const [importResult, setImportResult] = useState<string | null>(null);

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
    setSearchError(null);
    try {
      await api.addSoloKaraokeLibraryTrack({
        spotifyUri: r.uri,
        artist: r.artist,
        song: r.song,
        albumArt: r.albumArt,
        durationMs: r.durationMs,
      });
      onRefresh();
    } catch (e) {
      setSearchError(e instanceof Error ? e.message : "Failed to add track");
    } finally {
      setAdding((s) => {
        const n = new Set(s);
        n.delete(r.uri);
        return n;
      });
    }
  };

  const handleImport = async () => {
    if (!importUri.trim()) return;
    setImporting(true);
    setImportResult(null);
    try {
      const res = await api.importSoloKaraokePlaylist(importUri.trim());
      setImportResult(`Imported ${res.imported} tracks (${res.skipped} duplicates skipped)`);
      setImportUri("");
      onRefresh();
    } catch (e) {
      setImportResult(`Error: ${e instanceof Error ? e.message : "unknown"}`);
    } finally {
      setImporting(false);
    }
  };

  return (
    <div className="flex h-full flex-col gap-3 overflow-hidden border-r border-edge p-3">
      <h3 className="text-sm font-semibold text-slate-300">Library</h3>
      <p className="text-[11px] text-slate-500">
        Search or import, then drag into the Playlist to queue a song.
      </p>

      <input
        type="text"
        placeholder="Search Spotify by artist or song..."
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          search(e.target.value);
        }}
        className="rounded border border-edge bg-panel px-2 py-1 text-sm text-slate-200 placeholder-slate-500 outline-none focus:border-accent"
      />
      {loading && <div className="text-xs text-slate-500">Searching...</div>}
      {searchError && <div className="text-[11px] text-red-400">{searchError}</div>}
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

      <div className="flex gap-1">
        <input
          type="text"
          placeholder="Paste Spotify playlist link or URI..."
          value={importUri}
          onChange={(e) => setImportUri(e.target.value)}
          className="min-w-0 flex-1 rounded border border-edge bg-panel px-2 py-1 text-sm text-slate-200 placeholder-slate-500 outline-none focus:border-accent"
        />
        <button
          onClick={handleImport}
          disabled={importing || !importUri.trim()}
          className="shrink-0 rounded bg-accent/20 px-3 py-1 text-sm text-accent hover:bg-accent/30 disabled:opacity-50"
        >
          {importing ? "Importing..." : "Import"}
        </button>
      </div>
      {importResult && (
        <div className={`text-xs ${importResult.startsWith("Error") ? "text-red-400" : "text-green-400"}`}>
          {importResult}
        </div>
      )}

      <div className="text-xs text-slate-500">
        {tracks.length} {tracks.length === 1 ? "track" : "tracks"} in the library
      </div>

      <Droppable droppableId="skLibrary">
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
                Search or import above to add songs here.
              </div>
            )}
            {tracks.map((t, i) => (
              <TrackCard
                key={t.id}
                track={t}
                index={i}
                onDelete={onDeleteTrack}
                onRequeryLyrics={onRequeryLyrics}
                onOpenManualLyrics={onOpenManualLyrics}
                requeryingLyrics={requeryingId === t.id}
              />
            ))}
            {provided.placeholder}
          </div>
        )}
      </Droppable>
    </div>
  );
}

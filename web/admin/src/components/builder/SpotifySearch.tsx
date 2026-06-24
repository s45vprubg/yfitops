import { useState, useRef, useCallback } from "react";
import type { AdminApi, SpotifyResult } from "../../useAdminApi";

interface Props {
  api: AdminApi;
  boardId: string;
  onTrackAdded: () => void;
}

export default function SpotifySearch({ api, boardId, onTrackAdded }: Props) {
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
          const res = await api.searchSpotify(q, 10);
          setResults(res);
        } catch {
          setResults([]);
        } finally {
          setLoading(false);
        }
      }, 300);
    },
    [api]
  );

  const addTrack = async (r: SpotifyResult) => {
    setAdding((s) => new Set(s).add(r.uri));
    try {
      await api.addTrack(boardId, {
        spotifyUri: r.uri,
        artist: r.artist,
        song: r.song,
        albumArt: r.albumArt,
        durationMs: r.durationMs,
      });
      onTrackAdded();
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

  return (
    <div className="flex flex-col gap-2">
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
      {results.length > 0 && (
        <div className="max-h-48 overflow-y-auto rounded border border-edge bg-panel">
          {results.map((r) => (
            <div
              key={r.uri}
              className="flex items-center gap-2 border-b border-edge px-2 py-1 last:border-0"
            >
              {r.albumArt && (
                <img src={r.albumArt} alt="" className="h-8 w-8 rounded" />
              )}
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
    </div>
  );
}

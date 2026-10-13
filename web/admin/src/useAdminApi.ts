import { HTTP_URL } from "./config";

export interface BoardSummary {
  id: string;
  name: string;
  cols: number;
  createdAt: number;
  updatedAt: number;
  dailyDoubleCount: number;
}

// Lyrics status, mirrors server/internal/admin's LyricsStatus* constants.
// "" means the song has never been attempted at all (treated as playable by
// default on the grid — see admin.LyricsStatusUnknown); "pending" means an
// attempt was made but errored transiently and should be retried; "not_found"
// means LRCLIB confirmed no lyrics exist; "resolved" means lyric text is
// stored and ready.
export type LyricsStatus = "" | "pending" | "not_found" | "resolved";

// A song in a board's standalone Daily Double bucket — independent of the
// grid's category Track library. Lyrics are a hard requirement here (no
// override): the server rejects adding a track whose song isn't resolved.
export interface DailyDoubleTrackData {
  id: string;
  boardId: string;
  spotifyUri: string;
  artist: string;
  song: string;
  albumArt: string;
  durationMs: number;
  createdAt: number;
  lyricsStatus: LyricsStatus;
}

export interface TrackData {
  id: string;
  boardId: string;
  spotifyUri: string;
  artist: string;
  song: string;
  albumArt: string;
  durationMs: number;
  createdAt: number;
  lyricsStatus: LyricsStatus;
  // Admin chose to allow this track to play even without synced lyrics.
  lyricsOverride: boolean;
}

// One row of the global lyrics cache, keyed by Spotify URI — returned by the
// requery/manual-entry endpoints so the caller can refresh a track's status
// without a full re-list.
export interface LyricsRecord {
  spotifyUri: string;
  status: LyricsStatus;
  source?: "lrclib" | "manual";
  lastError?: string;
}

export interface LayoutCell {
  row: number;
  col: number;
  category: string;
  tracks: TrackData[];
}

export interface Layout {
  cols: number;
  cells: LayoutCell[];
}

export interface SpotifyResult {
  uri: string;
  artist: string;
  song: string;
  albumArt: string;
  durationMs: number;
}

export interface ImportResult {
  imported: number;
  skipped: number;
  total: number;
}

function api(secret: string) {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${secret}`,
    "Content-Type": "application/json",
  };

  async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await fetch(`${HTTP_URL}${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) {
      const text = await res.text();
      throw new Error(`${res.status}: ${text}`);
    }
    if (res.status === 204) return undefined as T;
    return res.json();
  }

  return {
    // Boards
    listBoards: () => req<BoardSummary[]>("GET", "/api/boards"),
    createBoard: (name: string) => req<BoardSummary>("POST", "/api/boards", { name }),
    getBoard: (id: string) => req<BoardSummary>("GET", `/api/boards/${id}`),
    renameBoard: (id: string, name: string) => req<void>("PATCH", `/api/boards/${id}`, { name }),
    setDailyDoubleCount: (id: string, dailyDoubleCount: number) =>
      req<void>("PATCH", `/api/boards/${id}`, { dailyDoubleCount }),
    deleteBoard: (id: string) => req<void>("DELETE", `/api/boards/${id}`),

    // Tracks
    listTracks: (boardId: string) => req<TrackData[]>("GET", `/api/boards/${boardId}/tracks`),
    unplacedTracks: (boardId: string) => req<TrackData[]>("GET", `/api/boards/${boardId}/unplaced`),
    addTrack: (boardId: string, track: { spotifyUri: string; artist: string; song: string; albumArt: string; durationMs: number }) =>
      req<TrackData>("POST", `/api/boards/${boardId}/tracks`, track),
    deleteTrack: (boardId: string, trackId: string) => req<void>("DELETE", `/api/boards/${boardId}/tracks/${trackId}`),
    setTrackOverride: (boardId: string, trackId: string, override: boolean) =>
      req<void>("PATCH", `/api/boards/${boardId}/tracks/${trackId}/override`, { override }),
    // Probes only never-attempted songs; known-resolved/not_found songs are
    // left untouched (see server/internal/admin/tracks.go rescanLyrics).
    rescanLyrics: (boardId: string) =>
      req<{ attempted: number; resolved: number }>("POST", `/api/boards/${boardId}/rescan-lyrics`),
    // Retries LRCLIB for one track regardless of its current lyrics status.
    requeryLyrics: (boardId: string, trackId: string) =>
      req<LyricsRecord>("POST", `/api/boards/${boardId}/tracks/${trackId}/requery-lyrics`),
    // Admin-pasted LRC text for a song auto-fetch couldn't find.
    setLyricsManual: (boardId: string, trackId: string, lrcText: string) =>
      req<LyricsRecord>("PUT", `/api/boards/${boardId}/tracks/${trackId}/lyrics`, { lrcText }),
    aiBuild: (boardId: string) =>
      req<{ categories: number; placed: number; total: number }>("POST", `/api/boards/${boardId}/ai-build`),

    // Daily Double bucket (standalone song pool, independent of the grid)
    listDailyDoubleTracks: (boardId: string) => req<DailyDoubleTrackData[]>("GET", `/api/boards/${boardId}/daily-double-tracks`),
    addDailyDoubleTrack: (boardId: string, track: { spotifyUri: string; artist: string; song: string; albumArt: string; durationMs: number }) =>
      req<DailyDoubleTrackData>("POST", `/api/boards/${boardId}/daily-double-tracks`, track),
    deleteDailyDoubleTrack: (boardId: string, trackId: string) =>
      req<void>("DELETE", `/api/boards/${boardId}/daily-double-tracks/${trackId}`),

    // Layout
    getLayout: (boardId: string) => req<Layout>("GET", `/api/boards/${boardId}/layout`),
    addColumn: (boardId: string, category: string) => req<{ col: number; category: string }>("POST", `/api/boards/${boardId}/columns`, { category }),
    removeColumn: (boardId: string, col: number) => req<void>("DELETE", `/api/boards/${boardId}/columns/${col}`),
    renameCategory: (boardId: string, col: number, category: string) => req<void>("PATCH", `/api/boards/${boardId}/columns/${col}`, { category }),
    placeTrack: (boardId: string, row: number, col: number, trackId: string, pos: number) =>
      req<void>("PUT", `/api/boards/${boardId}/cells/${row}/${col}/tracks/${trackId}`, { pos }),
    unplaceTrack: (boardId: string, row: number, col: number, trackId: string) =>
      req<void>("DELETE", `/api/boards/${boardId}/cells/${row}/${col}/tracks/${trackId}`),

    // Game-time
    attachBoard: (boardId: string, sessionId: string) => req<{ status: string }>("POST", `/api/boards/${boardId}/attach`, { sessionId }),
    startGame: () => req<{ status: string }>("POST", "/api/game/start"),
    resetGame: () => req<{ status: string }>("POST", "/api/game/reset"),

    // Spotify
    searchSpotify: (q: string, limit = 10) => req<SpotifyResult[]>("GET", `/api/spotify/search?q=${encodeURIComponent(q)}&limit=${limit}`),
    importPlaylist: (boardId: string, playlistUri: string) => req<ImportResult>("POST", `/api/boards/${boardId}/import-playlist`, { playlistUri }),
  };
}

export type AdminApi = ReturnType<typeof api>;
export function createAdminApi(secret: string): AdminApi {
  return api(secret);
}

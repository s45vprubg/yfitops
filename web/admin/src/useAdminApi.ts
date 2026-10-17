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

// A song in one of Solo Karaoke's two standalone buckets (not board-scoped,
// unlike TrackData above). Mirrors server/internal/admin's SoloKaraokeTrack.
// Position is populated (and meaningful) only for playlist tracks; library
// tracks are ordered by createdAt, like TrackData. Lyrics gating is soft for
// BOTH buckets (unlike the Daily Double bucket's hard requirement) — adding
// a track never fails for missing/unresolved lyrics.
export interface SoloKaraokeTrack {
  id: string;
  spotifyUri: string;
  artist: string;
  song: string;
  albumArt: string;
  durationMs: number;
  createdAt: number;
  position: number; // playlist only; 0 for library tracks
  lyricsStatus: LyricsStatus;
}

// Result of a rescan-lyrics/requery-pending-lyrics bulk call. truncated means
// maxRescanTracksPerRequest was hit with more eligible tracks still
// remaining — the caller should re-run the same action to pick up the rest.
export interface RescanResult {
  attempted: number;
  resolved: number;
  truncated: boolean;
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
      req<RescanResult>("POST", `/api/boards/${boardId}/rescan-lyrics`),
    // Bulk-retries every track still stuck 'pending' (errored/rate-limited).
    // Resolved/not_found tracks are left untouched, same as rescanLyrics.
    requeryPendingLyrics: (boardId: string) =>
      req<RescanResult>("POST", `/api/boards/${boardId}/requery-pending-lyrics`),
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

    // Solo Karaoke — standalone Library (holding pool) + Playlist (ordered
    // queue the engine plays from), not board-scoped. Mirrors
    // server/internal/admin/solokaraoke.go's route shape.
    listSoloKaraokeLibrary: () => req<SoloKaraokeTrack[]>("GET", "/api/solo-karaoke/library"),
    addSoloKaraokeLibraryTrack: (track: { spotifyUri: string; artist: string; song: string; albumArt: string; durationMs: number }) =>
      req<SoloKaraokeTrack>("POST", "/api/solo-karaoke/library", track),
    deleteSoloKaraokeLibraryTrack: (trackId: string) => req<void>("DELETE", `/api/solo-karaoke/library/${trackId}`),
    listSoloKaraokePlaylist: () => req<SoloKaraokeTrack[]>("GET", "/api/solo-karaoke/playlist"),
    addSoloKaraokePlaylistTrack: (track: { spotifyUri: string; artist: string; song: string; albumArt: string; durationMs: number }) =>
      req<SoloKaraokeTrack>("POST", "/api/solo-karaoke/playlist", track),
    deleteSoloKaraokePlaylistTrack: (trackId: string) => req<void>("DELETE", `/api/solo-karaoke/playlist/${trackId}`),
    // Full ordered rewrite of the playlist's position column — same
    // "atomically replace everything" shape as the board layout, avoiding
    // off-by-one arithmetic on a partial reorder.
    reorderSoloKaraokePlaylist: (orderedIds: string[]) =>
      req<void>("PUT", "/api/solo-karaoke/playlist/reorder", { orderedIds }),
    // Lands imported tracks in the Library, never the Playlist — admin
    // drags from there afterward.
    importSoloKaraokePlaylist: (playlistUri: string) =>
      req<ImportResult>("POST", "/api/solo-karaoke/import-playlist", { playlistUri }),
    // Resolves trackId against EITHER bucket.
    requerySoloKaraokeLyrics: (trackId: string) =>
      req<LyricsRecord>("POST", `/api/solo-karaoke/tracks/${trackId}/requery-lyrics`),
    setSoloKaraokeLyricsManual: (trackId: string, lrcText: string) =>
      req<LyricsRecord>("PUT", `/api/solo-karaoke/tracks/${trackId}/lyrics`, { lrcText }),
  };
}

export type AdminApi = ReturnType<typeof api>;
export function createAdminApi(secret: string): AdminApi {
  return api(secret);
}

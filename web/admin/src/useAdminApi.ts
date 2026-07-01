import { HTTP_URL } from "./config";

export interface BoardSummary {
  id: string;
  name: string;
  cols: number;
  createdAt: number;
  updatedAt: number;
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
  // null = not yet probed; true/false = LRCLIB synced-lyric result.
  hasSyncedLyrics: boolean | null;
  // Admin chose to allow this track to play even without synced lyrics.
  lyricsOverride: boolean;
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
    deleteBoard: (id: string) => req<void>("DELETE", `/api/boards/${id}`),

    // Tracks
    listTracks: (boardId: string) => req<TrackData[]>("GET", `/api/boards/${boardId}/tracks`),
    unplacedTracks: (boardId: string) => req<TrackData[]>("GET", `/api/boards/${boardId}/unplaced`),
    addTrack: (boardId: string, track: { spotifyUri: string; artist: string; song: string; albumArt: string; durationMs: number }) =>
      req<TrackData>("POST", `/api/boards/${boardId}/tracks`, track),
    deleteTrack: (boardId: string, trackId: string) => req<void>("DELETE", `/api/boards/${boardId}/tracks/${trackId}`),
    setTrackOverride: (boardId: string, trackId: string, override: boolean) =>
      req<void>("PATCH", `/api/boards/${boardId}/tracks/${trackId}/override`, { override }),
    rescanLyrics: (boardId: string) =>
      req<{ checked: number; withLyrics: number }>("POST", `/api/boards/${boardId}/rescan-lyrics`),

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

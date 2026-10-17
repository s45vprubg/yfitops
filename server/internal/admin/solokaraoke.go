package admin

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/lyrics"
)

// solokaraoke.go serves the standalone Solo Karaoke Library (holding pool)
// and Playlist (ordered queue the engine plays from) — not board-scoped,
// unlike every other admin REST file. Mirrors tracks.go/dailydouble.go's
// shape; lyrics gating is soft for BOTH buckets (unlike Daily Double's hard
// 412 reject): a track lacking resolved lyrics may still be added, with the
// lyrics status reported for the UI to flag it.

func (h *Handler) listSoloKaraokeLibrary(w http.ResponseWriter, r *http.Request) {
	tracks, err := h.store.ListSoloKaraokeLibraryTracks(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (h *Handler) addSoloKaraokeLibraryTrack(w http.ResponseWriter, r *http.Request) {
	track, ok := h.decodeSoloKaraokeTrackBody(w, r)
	if !ok {
		return
	}
	track.ID = generateID("sklib")
	track.CreatedAt = time.Now().UnixMilli()

	h.probeSoloKaraokeLyrics(r.Context(), track.SpotifyURI, track.Artist, track.Song, track.DurationMs)

	if err := h.store.AddSoloKaraokeLibraryTrack(r.Context(), track); err != nil {
		if errors.Is(err, ErrSoloKaraokeLibraryTrackExists) {
			http.Error(w, "a solo karaoke library track with this spotifyUri already exists", http.StatusConflict)
			return
		}
		serverError(w, err)
		return
	}
	if rec, err := h.store.GetLyricsStatus(r.Context(), track.SpotifyURI); err == nil && rec != nil {
		track.LyricsStatus = rec.Status
	}
	writeJSON(w, http.StatusCreated, track)
}

func (h *Handler) deleteSoloKaraokeLibraryTrack(w http.ResponseWriter, r *http.Request) {
	trackID := r.PathValue("trackId")
	if err := h.store.RemoveSoloKaraokeLibraryTrack(r.Context(), trackID); err != nil {
		if errors.Is(err, ErrSoloKaraokeLibraryTrackNotFound) {
			http.Error(w, "solo karaoke library track not found", http.StatusNotFound)
			return
		}
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listSoloKaraokePlaylist(w http.ResponseWriter, r *http.Request) {
	tracks, err := h.store.ListSoloKaraokePlaylistTracks(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (h *Handler) addSoloKaraokePlaylistTrack(w http.ResponseWriter, r *http.Request) {
	track, ok := h.decodeSoloKaraokeTrackBody(w, r)
	if !ok {
		return
	}
	track.ID = generateID("skpl")
	track.CreatedAt = time.Now().UnixMilli()

	h.probeSoloKaraokeLyrics(r.Context(), track.SpotifyURI, track.Artist, track.Song, track.DurationMs)

	if err := h.store.AddSoloKaraokePlaylistTrack(r.Context(), track); err != nil {
		if errors.Is(err, ErrSoloKaraokePlaylistTrackExists) {
			http.Error(w, "a solo karaoke playlist track with this spotifyUri already exists", http.StatusConflict)
			return
		}
		serverError(w, err)
		return
	}
	if rec, err := h.store.GetLyricsStatus(r.Context(), track.SpotifyURI); err == nil && rec != nil {
		track.LyricsStatus = rec.Status
	}
	h.reloadSoloKaraokeQueue(r.Context())
	writeJSON(w, http.StatusCreated, track)
}

func (h *Handler) deleteSoloKaraokePlaylistTrack(w http.ResponseWriter, r *http.Request) {
	trackID := r.PathValue("trackId")
	if err := h.store.RemoveSoloKaraokePlaylistTrack(r.Context(), trackID); err != nil {
		if errors.Is(err, ErrSoloKaraokePlaylistTrackNotFound) {
			http.Error(w, "solo karaoke playlist track not found", http.StatusNotFound)
			return
		}
		serverError(w, err)
		return
	}
	h.reloadSoloKaraokeQueue(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) reorderSoloKaraokePlaylist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrderedIDs []string `json:"orderedIds"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := h.store.ReorderSoloKaraokePlaylist(r.Context(), body.OrderedIDs); err != nil {
		serverError(w, err)
		return
	}
	h.reloadSoloKaraokeQueue(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// decodeSoloKaraokeTrackBody decodes + validates the shared request shape
// for adding a track to either bucket (identical validation to tracks.go's
// addTrack/dailydouble.go's addDailyDoubleTrack, minus the latter's hard
// lyrics-precondition check — gating is soft here). Writes the HTTP error
// itself and returns ok=false on any validation failure.
func (h *Handler) decodeSoloKaraokeTrackBody(w http.ResponseWriter, r *http.Request) (*SoloKaraokeTrack, bool) {
	var body struct {
		SpotifyURI string `json:"spotifyUri"`
		Artist     string `json:"artist"`
		Song       string `json:"song"`
		AlbumArt   string `json:"albumArt"`
		DurationMs int64  `json:"durationMs"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return nil, false
	}
	if !spotifyURIPattern.MatchString(body.SpotifyURI) {
		http.Error(w, "spotifyUri must match spotify:track:<id>", http.StatusBadRequest)
		return nil, false
	}
	if body.Artist == "" || body.Song == "" {
		http.Error(w, "artist and song are required", http.StatusBadRequest)
		return nil, false
	}
	if len(body.Artist) > 200 || len(body.Song) > 200 {
		http.Error(w, "artist/song too long (max 200)", http.StatusBadRequest)
		return nil, false
	}
	if body.DurationMs <= 0 || body.DurationMs > 600000 {
		http.Error(w, "durationMs must be 1-600000", http.StatusBadRequest)
		return nil, false
	}
	return &SoloKaraokeTrack{
		SpotifyURI: body.SpotifyURI,
		Artist:     body.Artist,
		Song:       body.Song,
		AlbumArt:   body.AlbumArt,
		DurationMs: body.DurationMs,
	}, true
}

// probeSoloKaraokeLyrics mirrors tracks.go's probeLyrics: a single LRCLIB
// probe IF this exact song has never been attempted before, shared by both
// buckets since lyrics are keyed globally by spotifyUri.
func (h *Handler) probeSoloKaraokeLyrics(ctx context.Context, spotifyURI, artist, song string, durationMs int64) {
	if h.lyrics == nil {
		return
	}
	if rec, err := h.store.GetLyricsStatus(ctx, spotifyURI); err != nil || rec != nil {
		return
	}
	h.probeOne(ctx, spotifyURI, artist, song, int(durationMs/1000))
}

// reloadSoloKaraokeQueue re-fetches the full ordered Playlist from the store
// and pushes it to the live engine, keeping its in-memory queue in sync
// after every playlist mutation (add/delete/reorder/import) without the
// engine ever querying Postgres itself. No-ops if no reloader is wired
// (dev/in-memory mode) — logs rather than fails the HTTP response either
// way, since the write to Postgres already succeeded.
func (h *Handler) reloadSoloKaraokeQueue(ctx context.Context) {
	if h.soloKaraoke == nil {
		return
	}
	rows, err := h.store.ListSoloKaraokePlaylistTracks(ctx)
	if err != nil {
		log.Printf("solo karaoke: reload queue: list playlist: %v", err)
		return
	}
	tracks := make([]*game.Track, len(rows))
	for i, t := range rows {
		tracks[i] = &game.Track{
			ID:         t.ID,
			SpotifyURI: t.SpotifyURI,
			Artist:     t.Artist,
			Song:       t.Song,
			AlbumArt:   t.AlbumArt,
			DurationMs: t.DurationMs,
			Playable:   true,
		}
	}
	h.soloKaraoke.ReloadSoloKaraokeQueue(tracks)
}

// importSoloKaraokePlaylist bulk-imports a Spotify playlist into the
// Library (the holding pool admin curates from — mirrors importPlaylist's
// board-scoped equivalent, which imports into a board's Track library, not
// directly onto the grid). Admin drag-selects into the Playlist afterward.
func (h *Handler) importSoloKaraokePlaylist(w http.ResponseWriter, r *http.Request) {
	if h.spotify == nil {
		http.Error(w, "Spotify not configured", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		PlaylistURI string `json:"playlistUri"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	playlistID := extractPlaylistID(body.PlaylistURI)
	if playlistID == "" {
		http.Error(w, "invalid playlist URI/URL", http.StatusBadRequest)
		return
	}

	results, err := h.spotify.GetPlaylistTracks(r.Context(), playlistID)
	if err != nil {
		log.Printf("admin: import solo karaoke playlist: %v", err)
		http.Error(w, "Spotify request failed", http.StatusBadGateway)
		return
	}

	imported, skipped := 0, 0
	base := time.Now().UnixMilli()
	added := make([]*SoloKaraokeTrack, 0, len(results))
	for i, t := range results {
		track := &SoloKaraokeTrack{
			ID:         generateID("sklib"),
			SpotifyURI: t.URI,
			Artist:     t.Artist,
			Song:       t.Song,
			AlbumArt:   t.AlbumArt,
			DurationMs: t.DurationMs,
			CreatedAt:  base + int64(i),
		}
		if err := h.store.AddSoloKaraokeLibraryTrack(r.Context(), track); err != nil {
			skipped++
		} else {
			imported++
			added = append(added, track)
		}
	}

	// Probe lyrics for the freshly-added tracks in the background (same
	// rationale as importPlaylist: LRCLIB is seconds-per-track, don't hang
	// the import response on it).
	if h.lyrics != nil && len(added) > 0 {
		go func(tracks []*SoloKaraokeTrack) {
			ctx := context.Background()
			for i, t := range tracks {
				h.probeOne(ctx, t.SpotifyURI, t.Artist, t.Song, int(t.DurationMs/1000))
				if i < len(tracks)-1 {
					time.Sleep(lyricsProbeDelay)
				}
			}
		}(added)
	}

	writeJSON(w, http.StatusOK, map[string]int{
		"imported": imported,
		"skipped":  skipped,
		"total":    len(results),
	})
}

// requerySoloKaraokeLyrics re-probes LRCLIB for a single track's song
// regardless of current status, resolving trackID against EITHER bucket.
// Mirrors tracks.go's requeryLyrics.
func (h *Handler) requerySoloKaraokeLyrics(w http.ResponseWriter, r *http.Request) {
	if h.lyrics == nil {
		http.Error(w, "lyrics prober not configured", http.StatusServiceUnavailable)
		return
	}
	trackID := r.PathValue("trackId")
	t, err := h.store.GetSoloKaraokeTrack(r.Context(), trackID)
	if err != nil {
		serverError(w, err)
		return
	}
	if t == nil {
		http.Error(w, "track not found in either solo karaoke bucket", http.StatusNotFound)
		return
	}
	h.probeOne(r.Context(), t.SpotifyURI, t.Artist, t.Song, int(t.DurationMs/1000))

	rec, err := h.store.GetLyricsStatus(r.Context(), t.SpotifyURI)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// setSoloKaraokeLyricsManual accepts admin-pasted LRC text for a track's
// song, resolving trackID against EITHER bucket. Mirrors tracks.go's
// setLyricsManual.
func (h *Handler) setSoloKaraokeLyricsManual(w http.ResponseWriter, r *http.Request) {
	trackID := r.PathValue("trackId")
	t, err := h.store.GetSoloKaraokeTrack(r.Context(), trackID)
	if err != nil {
		serverError(w, err)
		return
	}
	if t == nil {
		http.Error(w, "track not found in either solo karaoke bucket", http.StatusNotFound)
		return
	}

	var body struct {
		LRCText string `json:"lrcText"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(lyrics.ParseLRC(body.LRCText)) == 0 {
		http.Error(w, "lrcText did not parse into any timed lines", http.StatusBadRequest)
		return
	}

	if err := h.store.UpsertLyricsResolved(r.Context(), t.SpotifyURI, body.LRCText, "manual"); err != nil {
		serverError(w, err)
		return
	}
	rec, err := h.store.GetLyricsStatus(r.Context(), t.SpotifyURI)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

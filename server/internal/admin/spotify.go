package admin

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var playlistIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

func (h *Handler) searchSpotify(w http.ResponseWriter, r *http.Request) {
	if h.spotify == nil {
		http.Error(w, "Spotify not configured", http.StatusServiceUnavailable)
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "q parameter required", http.StatusBadRequest)
		return
	}

	limit := 10
	if ls := r.URL.Query().Get("limit"); ls != "" {
		if l, err := strconv.Atoi(ls); err == nil {
			limit = l
		}
	}

	results, err := h.spotify.Search(r.Context(), query, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// spotifyToken serves a currently-valid Spotify access token to the Stage so
// its Web Playback SDK getOAuthToken callback can fetch a live token on demand.
// The client refreshes behind ValidToken when the cached token is near expiry,
// so audio survives a multi-hour game (Spotify access tokens die ~1h after
// issue regardless of activity). Gated by the admin Bearer secret like every
// other /api route — never exposed to mobile.
func (h *Handler) spotifyToken(w http.ResponseWriter, r *http.Request) {
	if h.spotify == nil {
		http.Error(w, "Spotify not configured", http.StatusServiceUnavailable)
		return
	}
	token, err := h.spotify.ValidToken(r.Context())
	if err != nil {
		// 409: authenticated to the API, but Spotify OAuth not completed yet.
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (h *Handler) importPlaylist(w http.ResponseWriter, r *http.Request) {
	if h.spotify == nil {
		http.Error(w, "Spotify not configured", http.StatusServiceUnavailable)
		return
	}

	boardID := r.PathValue("id")

	var body struct {
		PlaylistURI string `json:"playlistUri"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	playlistID := extractPlaylistID(body.PlaylistURI)
	if playlistID == "" {
		http.Error(w, "invalid playlist URI/URL", http.StatusBadRequest)
		return
	}

	tracks, err := h.spotify.GetPlaylistTracks(r.Context(), playlistID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	imported := 0
	skipped := 0
	// Stamp created_at as base+index so the library preserves the Spotify
	// playlist order (a tight loop would otherwise collide on the same
	// millisecond and lose ordering). Track lists sort by created_at ASC.
	//
	// NOTE: do NOT probe lyrics inline here — LRCLIB responds in ~seconds per
	// track, which would make a large import hang for minutes. Import fast, then
	// probe lyrics in the background (the client also has a "Check lyrics"
	// button). has_synced_lyrics stays NULL (= playable) until the probe lands.
	base := time.Now().UnixMilli()
	added := make([]*Track, 0, len(tracks))
	for i, t := range tracks {
		track := &Track{
			ID:         generateID("trk"),
			BoardID:    boardID,
			SpotifyURI: t.URI,
			Artist:     t.Artist,
			Song:       t.Song,
			AlbumArt:   t.AlbumArt,
			DurationMs: t.DurationMs,
			CreatedAt:  base + int64(i),
		}
		if err := h.store.AddTrack(r.Context(), track); err != nil {
			skipped++
		} else {
			imported++
			added = append(added, track)
		}
	}

	// Probe lyrics for the freshly-added tracks in the background so the import
	// response returns immediately.
	if h.lyrics != nil && len(added) > 0 {
		go h.probeLyricsBatch(added)
	}

	writeJSON(w, http.StatusOK, map[string]int{
		"imported": imported,
		"skipped":  skipped,
		"total":    len(tracks),
	})
}

// extractPlaylistID parses a Spotify playlist ID from various input formats:
//   - spotify:playlist:37i9dQZF1DXcBWIGoYBM5M
//   - https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M
//   - https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M?si=...
//   - 37i9dQZF1DXcBWIGoYBM5M (raw ID)
func extractPlaylistID(input string) string {
	input = strings.TrimSpace(input)

	if strings.HasPrefix(input, "spotify:playlist:") {
		id := strings.TrimPrefix(input, "spotify:playlist:")
		if playlistIDPattern.MatchString(id) {
			return id
		}
		return ""
	}

	if strings.Contains(input, "open.spotify.com/playlist/") {
		parts := strings.Split(input, "open.spotify.com/playlist/")
		if len(parts) < 2 {
			return ""
		}
		id := parts[1]
		if idx := strings.IndexByte(id, '?'); idx >= 0 {
			id = id[:idx]
		}
		if idx := strings.IndexByte(id, '/'); idx >= 0 {
			id = id[:idx]
		}
		if playlistIDPattern.MatchString(id) {
			return id
		}
		return ""
	}

	if playlistIDPattern.MatchString(input) && len(input) >= 10 {
		return input
	}

	return ""
}

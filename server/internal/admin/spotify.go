package admin

import (
	"log"
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
		log.Printf("admin: spotify search: %v", err)
		http.Error(w, "Spotify request failed", http.StatusBadGateway)
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
		writeJSON(w, http.StatusOK, map[string]any{"token": nil, "connected": false})
		return
	}
	token, err := h.spotify.ValidToken(r.Context())
	if err != nil {
		// Log it (s4-api-001): this branch is the ONLY signal that stage audio is
		// about to die, and it used to be silent — the stage just quietly reports
		// "not connected" and the operator has nothing to look at. Deliberately
		// still a 200/connected:false rather than an error status: the stage's
		// getOAuthToken callback treats a non-200 as fatal. Cadence is low (SDK
		// connect + ~hourly expiry + reconnects), not a hot path, so an
		// unthrottled log will not drown the boot output.
		log.Printf("admin: spotify token unavailable (stage audio will not play): %v", err)
		writeJSON(w, http.StatusOK, map[string]any{"token": nil, "connected": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "connected": true})
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
	if err := decodeJSON(w, r, &body); err != nil {
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
		log.Printf("admin: import playlist: %v", err)
		http.Error(w, "Spotify request failed", http.StatusBadGateway)
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
			Year:       t.Year,
			Genre:      t.Genre,
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

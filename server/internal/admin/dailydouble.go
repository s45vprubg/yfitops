package admin

import (
	"net/http"
	"time"
)

// dailydouble.go serves the board-scoped Daily Double bucket — a standalone
// song pool (§7 sidenote) independent of the grid's category Track library.
// Mirrors tracks.go's addTrack/listTracks/deleteTrack shape exactly.

func (h *Handler) listDailyDoubleTracks(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	tracks, err := h.store.ListDailyDoubleTracks(r.Context(), boardID)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (h *Handler) addDailyDoubleTrack(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")

	var body struct {
		SpotifyURI string `json:"spotifyUri"`
		Artist     string `json:"artist"`
		Song       string `json:"song"`
		AlbumArt   string `json:"albumArt"`
		DurationMs int64  `json:"durationMs"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if !spotifyURIPattern.MatchString(body.SpotifyURI) {
		http.Error(w, "spotifyUri must match spotify:track:<id>", http.StatusBadRequest)
		return
	}
	if body.Artist == "" || body.Song == "" {
		http.Error(w, "artist and song are required", http.StatusBadRequest)
		return
	}
	if len(body.Artist) > 200 || len(body.Song) > 200 {
		http.Error(w, "artist/song too long (max 200)", http.StatusBadRequest)
		return
	}
	if body.DurationMs <= 0 || body.DurationMs > 600000 {
		http.Error(w, "durationMs must be 1-600000", http.StatusBadRequest)
		return
	}

	track := &DailyDoubleTrack{
		ID:         generateID("ddt"),
		BoardID:    boardID,
		SpotifyURI: body.SpotifyURI,
		Artist:     body.Artist,
		Song:       body.Song,
		AlbumArt:   body.AlbumArt,
		DurationMs: body.DurationMs,
		CreatedAt:  time.Now().UnixMilli(),
	}
	if err := h.store.AddDailyDoubleTrack(r.Context(), track); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, track)
}

func (h *Handler) deleteDailyDoubleTrack(w http.ResponseWriter, r *http.Request) {
	trackID := r.PathValue("trackId")
	if err := h.store.RemoveDailyDoubleTrack(r.Context(), trackID); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

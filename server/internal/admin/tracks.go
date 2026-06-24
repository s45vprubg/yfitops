package admin

import (
	"net/http"
	"regexp"
	"time"
)

var spotifyURIPattern = regexp.MustCompile(`^spotify:track:[a-zA-Z0-9]+$`)

func (h *Handler) listTracks(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	tracks, err := h.store.ListTracks(r.Context(), boardID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (h *Handler) unplacedTracks(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	tracks, err := h.store.UnplacedTracks(r.Context(), boardID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (h *Handler) addTrack(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")

	var body struct {
		SpotifyURI string `json:"spotifyUri"`
		Artist     string `json:"artist"`
		Song       string `json:"song"`
		AlbumArt   string `json:"albumArt"`
		DurationMs int64  `json:"durationMs"`
	}
	if err := decodeJSON(r, &body); err != nil {
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

	track := &Track{
		ID:         generateID("trk"),
		BoardID:    boardID,
		SpotifyURI: body.SpotifyURI,
		Artist:     body.Artist,
		Song:       body.Song,
		AlbumArt:   body.AlbumArt,
		DurationMs: body.DurationMs,
		CreatedAt:  time.Now().UnixMilli(),
	}

	if err := h.store.AddTrack(r.Context(), track); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, track)
}

func (h *Handler) deleteTrack(w http.ResponseWriter, r *http.Request) {
	trackID := r.PathValue("trackId")
	if err := h.store.DeleteTrack(r.Context(), trackID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

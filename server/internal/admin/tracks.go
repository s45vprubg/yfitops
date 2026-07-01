package admin

import (
	"context"
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
	// Probe LRCLIB for synced lyrics at add time so the builder can grey out
	// karaoke-incompatible tracks immediately (best-effort; nil prober leaves
	// has_synced_lyrics NULL = treated as playable until a re-scan).
	h.probeLyrics(r.Context(), track)

	if err := h.store.AddTrack(r.Context(), track); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, track)
}

// probeLyrics sets track.HasSyncedLyrics from the lyrics prober, if one is wired.
func (h *Handler) probeLyrics(ctx context.Context, t *Track) {
	if h.lyrics == nil {
		return
	}
	has := h.lyrics.HasSyncedLyrics(ctx, t.Artist, t.Song, int(t.DurationMs/1000))
	t.HasSyncedLyrics = &has
}

func (h *Handler) deleteTrack(w http.ResponseWriter, r *http.Request) {
	trackID := r.PathValue("trackId")
	if err := h.store.DeleteTrack(r.Context(), trackID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setTrackOverride toggles the play-anyway override for a lyric-less track.
func (h *Handler) setTrackOverride(w http.ResponseWriter, r *http.Request) {
	trackID := r.PathValue("trackId")
	var body struct {
		Override bool `json:"override"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := h.store.SetTrackLyrics(r.Context(), trackID, nil, &body.Override); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rescanLyrics re-probes LRCLIB for every track on a board and persists the
// results. Useful because LRCLIB gains lyrics over time (and a probe can fail
// transiently at import). Returns a small summary.
func (h *Handler) rescanLyrics(w http.ResponseWriter, r *http.Request) {
	if h.lyrics == nil {
		http.Error(w, "lyrics prober not configured", http.StatusServiceUnavailable)
		return
	}
	boardID := r.PathValue("id")
	tracks, err := h.store.ListTracks(r.Context(), boardID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	checked, withLyrics := 0, 0
	for i := range tracks {
		t := &tracks[i]
		has := h.lyrics.HasSyncedLyrics(r.Context(), t.Artist, t.Song, int(t.DurationMs/1000))
		if err := h.store.SetTrackLyrics(r.Context(), t.ID, &has, nil); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		checked++
		if has {
			withLyrics++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"checked": checked, "withLyrics": withLyrics})
}

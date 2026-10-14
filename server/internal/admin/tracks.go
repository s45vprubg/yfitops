package admin

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/lyrics"
)

var spotifyURIPattern = regexp.MustCompile(`^spotify:track:[a-zA-Z0-9]+$`)

// lyricsProbeDelay is the pause between sequential LRCLIB probes in a batch
// (rescanLyrics, probeLyricsBatch). LRCLIB's docs ask batch/full-library-scan
// clients to send requests sequentially with a 200-500ms delay between them
// rather than hammering the API concurrently — this is documented guidance,
// not a guess.
//
// A var (not const) solely so tests exercising maxRescanTracksPerRequest's
// cap (which otherwise walks up to 100 iterations) can shrink it and avoid a
// real multi-second sleep; production code never reassigns it.
var lyricsProbeDelay = 300 * time.Millisecond

// maxRescanTracksPerRequest caps how many never-attempted songs a single
// rescan-lyrics call will probe (api7-002). Without a cap, lyricsProbeDelay
// times an unbounded ListTracks() result can block the HTTP request for a
// very long time with no progress feedback and no way to stop it short of a
// disconnect — there is no server-side request timeout configured anywhere
// in this repo (gameserver's http.ListenAndServe uses Go's zero-value
// defaults) that would otherwise cut it off. 100 is comfortably above any
// realistic single board's track library (boardRows=5 rows per column, see
// boards.go, times a handful of columns, plus the separate Daily Double
// bucket) while still bounding the worst case to roughly 100 LRCLIB round
// trips. When the cap is hit, the response's Truncated field tells the
// admin/frontend to re-run the rescan to pick up the rest.
const maxRescanTracksPerRequest = 100

func (h *Handler) listTracks(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	tracks, err := h.store.ListTracks(r.Context(), boardID)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (h *Handler) unplacedTracks(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	tracks, err := h.store.UnplacedTracks(r.Context(), boardID)
	if err != nil {
		serverError(w, err)
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
	// Probe LRCLIB for synced lyrics at add time (only if this exact song has
	// never been attempted before — see probeLyrics) so the builder can grey
	// out karaoke-incompatible tracks immediately.
	h.probeLyrics(r.Context(), track)

	if err := h.store.AddTrack(r.Context(), track); err != nil {
		serverError(w, err)
		return
	}
	if rec, err := h.store.GetLyricsStatus(r.Context(), track.SpotifyURI); err == nil && rec != nil {
		track.LyricsStatus = rec.Status
	}
	writeJSON(w, http.StatusCreated, track)
}

// probeLyrics runs a single LRCLIB probe for t's song IF it has never been
// attempted before (no track_lyrics row yet for its spotifyUri) and persists
// the result to the global lyrics cache. A song that's already been checked
// — on any other board, or by an earlier import — is left alone; it does not
// get re-probed here (see probeOne for the write side, rescanLyrics/requery
// for the explicit retry paths).
func (h *Handler) probeLyrics(ctx context.Context, t *Track) {
	if h.lyrics == nil {
		return
	}
	if rec, err := h.store.GetLyricsStatus(ctx, t.SpotifyURI); err != nil || rec != nil {
		return
	}
	h.probeOne(ctx, t.SpotifyURI, t.Artist, t.Song, int(t.DurationMs/1000))
}

// probeOne runs a single LRCLIB probe and writes the classified result into
// the global lyrics cache: a confirmed match -> resolved, a confirmed absence
// (404/empty) -> not_found, anything else (timeout, rate limit, transport
// failure) -> pending + the error message, so a transient failure never
// overwrites a previously-resolved or previously-not_found row with a false
// negative (the bug this whole cache replaces).
func (h *Handler) probeOne(ctx context.Context, spotifyURI, artist, song string, durationSec int) {
	text, found, err := h.lyrics.Probe(ctx, artist, song, durationSec)
	switch {
	case err != nil:
		_ = h.store.UpsertLyricsError(ctx, spotifyURI, err.Error())
	case !found:
		_ = h.store.UpsertLyricsNotFound(ctx, spotifyURI)
	default:
		_ = h.store.UpsertLyricsResolved(ctx, spotifyURI, text, "lrclib")
	}
}

// probeLyricsBatch probes LRCLIB for many tracks' songs, skipping any whose
// spotifyUri has already been attempted, off the request path (used after a
// playlist import so the import itself returns fast). Requests are sent
// sequentially with a delay per LRCLIB's documented batch-scan guidance (see
// lyricsProbeDelay) rather than a concurrent worker pool, which is what let a
// big rescan hammer LRCLIB into rate-limiting itself. Uses a fresh background
// context — the HTTP request is already done by the time this runs.
func (h *Handler) probeLyricsBatch(tracks []*Track) {
	if h.lyrics == nil {
		return
	}
	ctx := context.Background()
	uris := make([]string, len(tracks))
	for i, t := range tracks {
		uris[i] = t.SpotifyURI
	}
	existing, err := h.store.GetLyricsStatusesForURIs(ctx, uris)
	if err != nil {
		existing = nil
	}
	for i, t := range tracks {
		if _, ok := existing[t.SpotifyURI]; ok {
			continue
		}
		h.probeOne(ctx, t.SpotifyURI, t.Artist, t.Song, int(t.DurationMs/1000))
		if i < len(tracks)-1 {
			time.Sleep(lyricsProbeDelay)
		}
	}
}

func (h *Handler) deleteTrack(w http.ResponseWriter, r *http.Request) {
	trackID := r.PathValue("trackId")
	if err := h.store.DeleteTrack(r.Context(), trackID); err != nil {
		serverError(w, err)
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
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := h.store.SetTrackOverride(r.Context(), trackID, body.Override); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rescanLyrics probes LRCLIB only for board tracks whose song has NEVER been
// attempted before (no track_lyrics row at all). Known-resolved and
// known-not_found songs are left alone — unlike the old implementation, this
// can never overwrite a previously-correct result with a transient-error
// false negative. A song that errored or came back not_found needs the
// explicit per-track requeryLyrics action instead (see below), by design: a
// bulk click shouldn't keep re-spending LRCLIB calls on songs it already has
// an answer for every time it's clicked.
//
// Probes run sequentially with lyricsProbeDelay between them per LRCLIB's
// documented batch-scan guidance. Returns a small summary.
func (h *Handler) rescanLyrics(w http.ResponseWriter, r *http.Request) {
	if h.lyrics == nil {
		http.Error(w, "lyrics prober not configured", http.StatusServiceUnavailable)
		return
	}
	boardID := r.PathValue("id")
	tracks, err := h.store.ListTracks(r.Context(), boardID)
	if err != nil {
		serverError(w, err)
		return
	}

	uris := make([]string, len(tracks))
	for i, t := range tracks {
		uris[i] = t.SpotifyURI
	}
	existing, err := h.store.GetLyricsStatusesForURIs(r.Context(), uris)
	if err != nil {
		serverError(w, err)
		return
	}

	attempted, resolved, truncated := 0, 0, false
	for i, t := range tracks {
		// api7-001: stop the moment the client's request context is gone
		// (disconnect/cancel) instead of grinding through every remaining
		// track. A track that was never reached must stay in its true
		// never-attempted state — no row at all — rather than getting a
		// bogus pending/"context canceled" row via UpsertLyricsError below
		// (the ON CONFLICT UPDATE guard in that upsert only protects rows
		// that already exist; a brand-new row's bare INSERT path has no
		// such guard).
		if r.Context().Err() != nil {
			break
		}
		if _, ok := existing[t.SpotifyURI]; ok {
			continue
		}
		// api7-002: cap how many never-attempted songs one request will
		// probe. See maxRescanTracksPerRequest's doc comment for why.
		if attempted >= maxRescanTracksPerRequest {
			truncated = true
			break
		}
		text, found, perr := h.lyrics.Probe(r.Context(), t.Artist, t.Song, int(t.DurationMs/1000))
		attempted++
		switch {
		case perr != nil:
			_ = h.store.UpsertLyricsError(r.Context(), t.SpotifyURI, perr.Error())
		case !found:
			_ = h.store.UpsertLyricsNotFound(r.Context(), t.SpotifyURI)
		default:
			_ = h.store.UpsertLyricsResolved(r.Context(), t.SpotifyURI, text, "lrclib")
			resolved++
		}
		if i < len(tracks)-1 {
			// Interruptible: a disconnect mid-sleep stops immediately
			// instead of waiting out the full delay before the next
			// iteration's context check even gets a chance to run.
			select {
			case <-time.After(lyricsProbeDelay):
			case <-r.Context().Done():
			}
		}
	}
	writeJSON(w, http.StatusOK, rescanLyricsResult{Attempted: attempted, Resolved: resolved, Truncated: truncated})
}

// rescanLyricsResult summarizes one rescan-lyrics call. Truncated is true
// when maxRescanTracksPerRequest was hit with never-attempted tracks still
// remaining (api7-002) — the admin/frontend should re-run the rescan to pick
// up the rest. It is NOT set when the loop stopped early due to a cancelled
// request context (api7-001): in that case nothing is listening for the
// response anyway.
type rescanLyricsResult struct {
	Attempted int  `json:"attempted"`
	Resolved  int  `json:"resolved"`
	Truncated bool `json:"truncated"`
}

// requeryLyrics re-probes LRCLIB for a single track's song regardless of its
// current status — the explicit retry path for a song that previously came
// back not_found or errored (pending). Writes the same resolved/not_found/
// pending+error outcome as probeOne/rescanLyrics.
func (h *Handler) requeryLyrics(w http.ResponseWriter, r *http.Request) {
	if h.lyrics == nil {
		http.Error(w, "lyrics prober not configured", http.StatusServiceUnavailable)
		return
	}
	boardID := r.PathValue("id")
	trackID := r.PathValue("trackId")
	t, err := h.store.GetTrack(r.Context(), trackID)
	if err != nil {
		serverError(w, err)
		return
	}
	// api7-003: scope the lookup to the board in the URL, mirroring
	// dailydouble.go's deleteDailyDoubleTrack/RemoveDailyDoubleTrack
	// board-scoped pattern (dd-api-2) — a trackId from board A must not
	// resolve through board B's URL.
	if t == nil || t.BoardID != boardID {
		http.Error(w, "track not found on this board", http.StatusNotFound)
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

// setLyricsManual accepts admin-pasted LRC text for a track's song when
// auto-fetch can't find it, stores it in the global lyrics cache as the
// resolved source of truth (source="manual"), and shares the SAME storage
// every other lyrics-aware code path reads from — a manually-entered song
// gets the identical karaoke overlay a successful LRCLIB fetch would have.
func (h *Handler) setLyricsManual(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	trackID := r.PathValue("trackId")
	t, err := h.store.GetTrack(r.Context(), trackID)
	if err != nil {
		serverError(w, err)
		return
	}
	// api7-003: scope the lookup to the board in the URL (see requeryLyrics's
	// identical check above for the full rationale).
	if t == nil || t.BoardID != boardID {
		http.Error(w, "track not found on this board", http.StatusNotFound)
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

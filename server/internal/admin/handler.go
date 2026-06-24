package admin

import (
	"encoding/json"
	"net/http"
)

// Handler serves the admin REST API for track and board management.
type Handler struct {
	store   AdminStore
	spotify SpotifySearcher
	engine  BoardReloader
	secret  string
}

// NewHandler creates a Handler. Pass nil for spotify if Spotify is unavailable
// (search endpoints will return 503).
func NewHandler(store AdminStore, spotify SpotifySearcher, engine BoardReloader, secret string) *Handler {
	return &Handler{
		store:   store,
		spotify: spotify,
		engine:  engine,
		secret:  secret,
	}
}

// Register mounts all admin API routes onto the given mux. CORS is handled
// at the top level via a catch-all /api/ prefix handler that intercepts OPTIONS
// preflight requests before method-based routing.
func (h *Handler) Register(mux *http.ServeMux) {
	auth := AdminAuth(h.secret)
	wrap := func(handler http.HandlerFunc) http.Handler {
		return auth(handler)
	}

	// Boards
	mux.Handle("GET /api/boards", wrap(h.listBoards))
	mux.Handle("POST /api/boards", wrap(h.createBoard))
	mux.Handle("GET /api/boards/{id}", wrap(h.getBoard))
	mux.Handle("PATCH /api/boards/{id}", wrap(h.renameBoard))
	mux.Handle("DELETE /api/boards/{id}", wrap(h.deleteBoard))

	// Tracks
	mux.Handle("GET /api/boards/{id}/tracks", wrap(h.listTracks))
	mux.Handle("GET /api/boards/{id}/unplaced", wrap(h.unplacedTracks))
	mux.Handle("POST /api/boards/{id}/tracks", wrap(h.addTrack))
	mux.Handle("DELETE /api/boards/{id}/tracks/{trackId}", wrap(h.deleteTrack))

	// Layout
	mux.Handle("GET /api/boards/{id}/layout", wrap(h.getLayout))
	mux.Handle("POST /api/boards/{id}/columns", wrap(h.addColumn))
	mux.Handle("DELETE /api/boards/{id}/columns/{col}", wrap(h.removeColumn))
	mux.Handle("PATCH /api/boards/{id}/columns/{col}", wrap(h.renameCategory))
	mux.Handle("PUT /api/boards/{id}/cells/{row}/{col}/tracks/{trackId}", wrap(h.placeTrack))
	mux.Handle("DELETE /api/boards/{id}/cells/{row}/{col}/tracks/{trackId}", wrap(h.unplaceTrack))

	// Game-time
	mux.Handle("POST /api/boards/{id}/attach", wrap(h.attachBoard))
	mux.Handle("POST /api/game/start", wrap(h.startGame))

	// Spotify
	mux.Handle("GET /api/spotify/search", wrap(h.searchSpotify))
	mux.Handle("POST /api/boards/{id}/import-playlist", wrap(h.importPlaylist))
	// Note: GET /api/spotify/token is registered separately via
	// RegisterSpotifyToken so it works without Postgres (the Stage needs it in
	// in-memory dev mode too).
}

// RegisterSpotifyToken mounts ONLY the GET /api/spotify/token route, which
// serves a live Spotify access token to the Stage's Web Playback SDK. It is
// split out of Register because it depends solely on the Spotify client, not
// the board store — so the entrypoint can expose it even when Postgres (and
// thus the rest of the admin API) is unavailable. Gated by the admin secret.
func RegisterSpotifyToken(mux *http.ServeMux, spotify SpotifySearcher, secret string) {
	h := &Handler{spotify: spotify, secret: secret}
	mux.Handle("GET /api/spotify/token", AdminAuth(secret)(http.HandlerFunc(h.spotifyToken)))
}

// CORSHandler returns an http.Handler that wraps the given handler with CORS
// headers. Use this at the top level in main.go to wrap the entire mux.
func CORSHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// JSON helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

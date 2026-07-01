// Command gameserver is the yfitops V2 backend entrypoint. It wires the fixed
// contracts and component packages into a running server (design_doc §2):
//
//	transport (WebTransport/QUIC)  -> game.Engine -> { Redis lock, Postgres repo,
//	                                                   Spotify device, LRCLIB }
//
// Data layer (Redis/Postgres) is optional at boot: if unreachable, the server
// falls back to in-memory implementations and a seeded sample board so the
// system runs and is demonstrable without the full infra. A clear log line
// states which mode each subsystem is in — no silent degradation.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/s45vprubg/yfitops/server/internal/admin"
	"github.com/s45vprubg/yfitops/server/internal/anticheat"
	"github.com/s45vprubg/yfitops/server/internal/config"
	"github.com/s45vprubg/yfitops/server/internal/game"
	"github.com/s45vprubg/yfitops/server/internal/lyrics"
	"github.com/s45vprubg/yfitops/server/internal/spotify"
	"github.com/s45vprubg/yfitops/server/internal/store"
	"github.com/s45vprubg/yfitops/server/internal/transport"
)

func main() {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ---- Data layer (graceful fallback to in-memory) ----
	var lock game.BuzzLock
	if rl, err := store.NewRedisLock(cfg.RedisAddr); err == nil {
		lock = rl
		log.Printf("buzz lock: Redis @ %s", cfg.RedisAddr)
	} else {
		lock = store.NewMemLock()
		log.Printf("buzz lock: IN-MEMORY (Redis unavailable: %v)", err)
	}

	var repo game.GameRepo
	var needSampleBoard bool
	if pr, err := store.NewPostgresRepo(ctx, cfg.PostgresDSN); err == nil {
		repo = pr
		log.Printf("repo: Postgres")
		if b, berr := pr.LoadBoard(ctx, cfg.SessionID()); berr != nil || b == nil {
			log.Printf("repo: no board attached for session %q — use Board Builder to create and load one", cfg.SessionID())
		}
	} else {
		mr := store.NewMemRepo()
		mr.SeedSampleBoard(cfg.SessionID())
		repo = mr
		needSampleBoard = true
		log.Printf("repo: IN-MEMORY + sample board (Postgres unavailable: %v)", err)
	}

	// ---- External services ----
	audio := spotify.New(cfg)
	lyr := lyrics.New(cfg)

	// Spotify refresh-token persistence (dev convenience): reload a previously
	// saved refresh token so a server restart does NOT force a fresh OAuth
	// dance. The refresh token is the durable credential — ValidToken mints a
	// live access token from it on demand. Path is overridable; defaults next
	// to the cert dir. Best-effort: a missing/unreadable file is fine (just
	// means "re-auth needed").
	tokenPath := os.Getenv("YFI_SPOTIFY_TOKEN_FILE")
	if tokenPath == "" {
		tokenPath = filepath.Join(filepath.Dir(cfg.CertFile), "spotify_refresh_token")
	}
	spotifyRestored := false
	if data, err := os.ReadFile(tokenPath); err == nil {
		if rt := strings.TrimSpace(string(data)); rt != "" {
			audio.RestoreRefreshToken(rt)
			spotifyRestored = true
			log.Printf("spotify: restored refresh token from %s", tokenPath)
		}
	}

	// ---- Anti-cheat nonce gate (§4D) ----
	gate := anticheat.NewNonceGate([]byte(cfg.NonceSecret))

	// ---- Transport hub (Broadcaster) ----
	hub := transport.NewHub()

	// ---- Engine ----
	// Reveal-timing knob defaults (config.go is a locked contract file, so these
	// are read from env here rather than added there). They only seed the
	// initial values; the control room can tune them live.
	revAlt, revAltSet := envBool("YFI_REVEAL_ALTERNATE")
	revEase, revEaseSet := envIntSet("YFI_REVEAL_EASE_MS")
	eng := game.NewEngine(repo, lock, audio, lyr, hub, gate, game.Config{
		SessionID:          cfg.SessionID(),
		AdminSecret:        cfg.AdminSecret,
		SkipThresholdPct:   cfg.DefaultSkipThresholdPct,
		RevealIntervalMs:   envInt("YFI_REVEAL_INTERVAL_MS", 0),
		RevealPhase1Ms:     envInt("YFI_REVEAL_PHASE1_MS", 0),
		RevealBlockMs:      envInt("YFI_REVEAL_BLOCK_MS", 0),
		RevealEaseMs:       revEase,
		RevealEaseSet:      revEaseSet,
		RevealAlternate:    revAlt,
		RevealAlternateSet: revAltSet,
	})
	if needSampleBoard {
		eng.SetBoard(store.SampleBoard())
		log.Printf("engine: sample board injected (5×5, demo tracks)")
	}
	// If we restored a persisted Spotify refresh token, mark Spotify authed so a
	// connecting stage is told to initialize the Web Playback SDK (registers the
	// playback device). Without this a restored-token boot yields NO_ACTIVE_DEVICE.
	if spotifyRestored {
		eng.MarkSpotifyAuthed()
	}
	eng.SetRoleSetter(hub) // promote roles on validated Hello (§4A)
	go func() {
		if err := eng.Run(ctx); err != nil && ctx.Err() == nil {
			log.Fatalf("engine: %v", err)
		}
	}()

	// ---- WebTransport server ----
	srv, err := transport.NewServer(cfg, hub, eng)
	if err != nil {
		log.Fatalf("transport: %v", err)
	}

	// ---- Plain HTTP: health, Spotify OAuth, cert hash for dev clients ----
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Spotify OAuth (§6, §9): admin opens /auth/spotify on the stage tab.
	// CSRF protection: mint a random state, stash it in a short-lived cookie,
	// and require an exact match on the callback (replaces the old constant
	// "yfitops" state, which offered no CSRF protection).
	const stateCookie = "yfi_spotify_state"
	mux.HandleFunc("/auth/spotify", func(w http.ResponseWriter, r *http.Request) {
		state, err := randomState()
		if err != nil {
			http.Error(w, "state error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     stateCookie,
			Value:    state,
			Path:     "/auth/spotify",
			MaxAge:   600, // 10 minutes to complete the dance
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, audio.AuthURL(state), http.StatusFound)
	})
	mux.HandleFunc("/auth/spotify/callback", func(w http.ResponseWriter, r *http.Request) {
		// Verify the state matches the cookie (constant-time) before doing
		// anything with the code.
		want, err := r.Cookie(stateCookie)
		got := r.URL.Query().Get("state")
		if err != nil || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want.Value)) != 1 {
			http.Error(w, "invalid OAuth state", http.StatusBadRequest)
			return
		}
		// Clear the one-time state cookie.
		http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: "", Path: "/auth/spotify", MaxAge: -1})

		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		token, err := audio.ExchangeToken(r.Context(), code)
		if err != nil {
			http.Error(w, "exchange failed: "+err.Error(), http.StatusBadGateway)
			return
		}
		eng.PushSpotifyToken(token)
		// Persist the refresh token so a server restart skips re-auth (dev
		// convenience). Best-effort — log on failure but don't fail the flow.
		if rt := audio.RefreshToken(); rt != "" {
			if werr := os.WriteFile(tokenPath, []byte(rt), 0o600); werr != nil {
				log.Printf("spotify: could not persist refresh token to %s: %v", tokenPath, werr)
			} else {
				log.Printf("spotify: persisted refresh token to %s", tokenPath)
			}
		}
		_, _ = w.Write([]byte("Spotify authenticated. Token pushed to Stage. You may close this tab."))
	})
	// Dev clients need the self-signed cert's SHA-256 for serverCertificateHashes.
	// NewServer above has already generated the cert if it was missing.
	if _, b64, herr := transport.CertSHA256(cfg.CertFile); herr == nil {
		mux.HandleFunc("/cert-hash", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			_, _ = w.Write([]byte(b64))
		})
	}

	// ---- Spotify token endpoint (always available) ----
	// The Stage's Web Playback SDK fetches a live token here on every
	// getOAuthToken call (refreshed server-side). It depends only on the
	// Spotify client, NOT the store, so it is registered even in the in-memory
	// dev mode (dev-up.sh) where the board-management admin API is skipped.
	admin.RegisterSpotifyToken(mux, &admin.SpotifyAdapter{Client: audio}, cfg.AdminSecret)

	// ---- Admin REST API (board/track management — needs Postgres) ----
	if pr, ok := repo.(*store.PostgresRepo); ok {
		spotifyAdapter := &admin.SpotifyAdapter{Client: audio}
		adminHandler := admin.NewHandler(pr, spotifyAdapter, eng, cfg.AdminSecret)
		adminHandler.SetLyricsProber(lyr) // probe synced-lyric availability on add/import
		adminHandler.Register(mux)
		log.Printf("admin API: registered on /api/*")
	} else {
		log.Printf("admin API: board management skipped (Postgres unavailable); /api/spotify/token still active")
	}

	go func() {
		log.Printf("HTTP (health/oauth/admin) on %s", cfg.HTTPAddr)
		if err := http.ListenAndServe(cfg.HTTPAddr, admin.CORSHandler(mux)); err != nil && ctx.Err() == nil {
			log.Printf("http server: %v", err)
		}
	}()

	log.Printf("WebTransport on %s (cert %s)", cfg.ListenAddr, cfg.CertFile)
	go func() {
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			log.Fatalf("webtransport: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	_ = srv.Close()
}

// randomState returns a cryptographically random hex string for the OAuth
// state parameter (CSRF protection on the Spotify callback).
func randomState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// envInt reads an int env var, returning def if unset or unparseable.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envBool reads a bool env var. The second return reports whether it was set,
// so an unset var leaves the engine default (true) untouched.
func envBool(key string) (val bool, set bool) {
	v := os.Getenv(key)
	if v == "" {
		return false, false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, false
	}
	return b, true
}

// envIntSet reads an int env var, reporting whether it was set — so a knob whose
// default is non-zero (e.g. ease=600) can be distinguished from an explicit 0.
func envIntSet(key string) (val int, set bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

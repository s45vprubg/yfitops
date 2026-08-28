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
	"github.com/s45vprubg/yfitops/server/internal/ai"
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

	// Resolved deployment mode, logged first so a misconfigured YFI_ENV is
	// visible in the opening lines rather than inferred from a missing warning.
	log.Printf("mode: %s (YFI_ENV=%q)", modeName(), os.Getenv("YFI_ENV"))

	// Production guard: refuse to boot with known dev-default secrets outside
	// dev. config.go is a locked contract file, so this gate lives at the
	// caller. It must NOT fire in dev (YFI_ENV unset/empty or "dev").
	//
	// In dev the same check still runs, but only to WARN. The guard is otherwise
	// invisible until someone leaves dev, which means a misconfigured event
	// server looks identical at boot to a correct one. A warning is the cheapest
	// way to make "your secrets are the published defaults" impossible to miss;
	// it must never be fatal in dev, because dropping a live server over a dev
	// default is worse than running with one.
	defaulted := defaultedSecrets(cfg)
	if len(defaulted) > 0 {
		if !isDev() {
			log.Fatalf("config: refusing to start outside dev (YFI_ENV=%q) with dev-default secret(s): %s — set a non-default value", os.Getenv("YFI_ENV"), strings.Join(defaulted, ", "))
		}
		log.Printf("WARNING: running with dev-default secret(s): %s — anyone can claim admin/stage. Set real values and YFI_ENV=prod before an event.", strings.Join(defaulted, ", "))
	}

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
			// Secure only over https — a Secure cookie is dropped by browsers on
			// plaintext http, which would break the local-dev OAuth dance.
			Secure: strings.HasPrefix(cfg.SpotifyRedirectURI, "https"),
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
		// Optional AI board builder (Gemini). Disabled if GEMINI_API_KEY absent.
		if gc := ai.New(os.Getenv("GEMINI_API_KEY"), os.Getenv("GEMINI_MODEL")); gc != nil {
			adminHandler.SetCategorizer(&geminiAdapter{c: gc})
			log.Printf("AI board builder: enabled (Gemini)")
		}
		adminHandler.Register(mux)
		log.Printf("admin API: registered on /api/*")
	} else {
		log.Printf("admin API: board management skipped (Postgres unavailable); /api/spotify/token still active")
	}

	// ---- Cleartext WebSocket fallback (phones on LAN without secure context) ----
	// WebTransport requires a secure context, so a phone on a plain-HTTP LAN
	// origin cannot use it at all. /ws carries the same framing over cleartext
	// WebSocket so real devices can join without standing up TLS.
	//
	// It takes TWO explicit vars — YFI_DEV_WS=1 and YFI_INSECURE_TRANSPORT=1 —
	// and is deliberately INDEPENDENT of YFI_ENV. Keying it off YFI_ENV made the
	// gate mutually exclusive with its own use case: the operator who needs the
	// LAN fallback had to stay out of prod, which also disarmed the
	// default-secret boot guard above. Now a prod server with real secrets can
	// serve the fallback if the operator explicitly acknowledges the cleartext
	// risk, and that acknowledgement is logged loudly.
	wsd := decideWS(os.Getenv("YFI_DEV_WS"), os.Getenv("YFI_INSECURE_TRANSPORT"), isProd())
	if wsd.msg != "" {
		log.Print(wsd.msg)
	}
	if wsd.register {
		wsHandler := transport.NewWSHandler(hub, eng, srv)
		mux.Handle("/ws", wsHandler)
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

// defaultedSecrets returns the names of the secrets still holding the dev
// defaults published in deploy/.env.example. The default literals are duplicated
// from config.go's env() fallbacks on purpose — config.go is a locked contract
// file, so this caller cannot ask it which values are defaults.
func defaultedSecrets(cfg *config.Config) []string {
	var out []string
	if cfg.AdminSecret == "changeme-admin" {
		out = append(out, "ADMIN_SECRET")
	}
	if cfg.NonceSecret == "dev-nonce-secret" {
		out = append(out, "YFI_NONCE_SECRET")
	}
	if cfg.JoinSecret == "dev-join-secret" {
		out = append(out, "YFI_JOIN_SECRET")
	}
	return out
}

// normEnv folds and trims YFI_ENV so the production gates cannot be defeated by
// casing or stray whitespace ("Prod", "PROD", " prod " all mean prod).
func normEnv(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// isProdEnv reports whether a raw YFI_ENV value names a production deployment.
// "production" counts too — it is the spelling operators reach for, and treating
// it as dev silently disarmed every gate.
func isProdEnv(v string) bool {
	n := normEnv(v)
	return n == "prod" || n == "production"
}

// isDevEnv reports whether a raw YFI_ENV value names a dev deployment. An
// unset/empty value MUST stay dev: docker-compose.yml defaults it to dev and
// scripts/dev-up.sh runs with it bare.
func isDevEnv(v string) bool {
	n := normEnv(v)
	return n == "" || n == "dev"
}

func isProd() bool { return isProdEnv(os.Getenv("YFI_ENV")) }
func isDev() bool  { return isDevEnv(os.Getenv("YFI_ENV")) }

// modeName is the resolved deployment mode for the boot log. Anything that is
// neither dev nor prod reports "non-dev" — the gates treat it as not-dev.
func modeName() string {
	switch {
	case isProd():
		return "prod"
	case isDev():
		return "dev"
	default:
		return "non-dev"
	}
}

// wsDecision is the outcome of the cleartext /ws registration matrix: whether to
// mount the route, and the single line to log about it (never empty when
// YFI_DEV_WS=1, so a declined route can't look like a broken build).
type wsDecision struct {
	register bool
	msg      string
}

// decideWS resolves the /ws registration matrix from the two opt-in vars and the
// resolved prod flag. Pure so the matrix is unit-testable.
func decideWS(devWS, insecureAck string, prod bool) wsDecision {
	if devWS != "1" {
		return wsDecision{}
	}
	if insecureAck != "1" {
		return wsDecision{msg: "NOTICE: YFI_DEV_WS=1 but /ws was NOT registered — the cleartext WebSocket fallback also requires the explicit acknowledgement YFI_INSECURE_TRANSPORT=1"}
	}
	if prod {
		return wsDecision{register: true, msg: "WARNING: cleartext /ws fallback REGISTERED IN PROD (YFI_DEV_WS=1 + YFI_INSECURE_TRANSPORT=1) — this transport has NO encryption and NO cert pinning: anyone on the LAN can read, forge and replay frames, including the admin/stage secret. Only use this on a network you trust; prefer TLS + WebTransport."}
	}
	return wsDecision{register: true, msg: "DEV: WebSocket fallback registered on /ws (YFI_DEV_WS=1 + YFI_INSECURE_TRANSPORT=1)"}
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

// geminiAdapter adapts *ai.Client to admin.Categorizer, converting between the
// two packages' track/proposal types (keeps the admin package free of an ai
// dependency).
type geminiAdapter struct{ c *ai.Client }

func (g *geminiAdapter) BuildCategories(ctx context.Context, tracks []admin.AITrack, rows, cols int) (*admin.AIProposal, error) {
	in := make([]ai.TrackInput, len(tracks))
	for i, t := range tracks {
		in[i] = ai.TrackInput{ID: t.ID, Artist: t.Artist, Song: t.Song}
	}
	p, err := g.c.BuildCategories(ctx, in, rows, cols)
	if err != nil {
		return nil, err
	}
	out := &admin.AIProposal{Categories: make([]admin.AICategory, len(p.Categories))}
	for i, c := range p.Categories {
		out.Categories[i] = admin.AICategory{Name: c.Name, TrackIDs: c.TrackIDs}
	}
	return out, nil
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

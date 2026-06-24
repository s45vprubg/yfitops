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
	"log"
	"net/http"
	"os"
	"os/signal"
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

	// ---- Anti-cheat nonce gate (§4D) ----
	gate := anticheat.NewNonceGate([]byte(cfg.NonceSecret))

	// ---- Transport hub (Broadcaster) ----
	hub := transport.NewHub()

	// ---- Engine ----
	eng := game.NewEngine(repo, lock, audio, lyr, hub, gate, game.Config{
		SessionID:        cfg.SessionID(),
		AdminSecret:      cfg.AdminSecret,
		SkipThresholdPct: cfg.DefaultSkipThresholdPct,
	})
	if needSampleBoard {
		eng.SetBoard(store.SampleBoard())
		log.Printf("engine: sample board injected (5×5, demo tracks)")
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
	mux.HandleFunc("/auth/spotify", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, audio.AuthURL("yfitops"), http.StatusFound)
	})
	mux.HandleFunc("/auth/spotify/callback", func(w http.ResponseWriter, r *http.Request) {
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

	// ---- Admin REST API (track/board management) ----
	if pr, ok := repo.(*store.PostgresRepo); ok {
		spotifyAdapter := &admin.SpotifyAdapter{Client: audio}
		adminHandler := admin.NewHandler(pr, spotifyAdapter, eng, cfg.AdminSecret)
		adminHandler.Register(mux)
		log.Printf("admin API: registered on /api/*")
	} else {
		log.Printf("admin API: skipped (Postgres unavailable)")
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

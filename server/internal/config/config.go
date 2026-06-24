// Package config centralizes all runtime configuration, loaded from env with
// sane defaults so the server boots in dev without a full secret set.
package config

import (
	"os"
	"strconv"
)

type Config struct {
	// Network
	ListenAddr string // WebTransport/HTTP3 listen address, e.g. ":4433"
	HTTPAddr   string // plain HTTP for health/static, e.g. ":8080"
	CertFile   string // TLS cert (required for HTTP/3 / WebTransport)
	KeyFile    string

	// Data layer
	RedisAddr   string
	PostgresDSN string

	// Spotify (design_doc §6, §9) — device routing via Web API
	SpotifyClientID     string
	SpotifyClientSecret string
	SpotifyRedirectURI  string

	// Lyrics (LRCLIB, design_doc §6)
	LRCLIBBaseURL string

	// Security
	AdminSecret string // gates /admin (design_doc §9)
	NonceSecret string // HMAC key for the nonce gate (§4D)
	JoinSecret  string // HMAC key for rotating QR join tokens (§3.1)

	// Gameplay defaults
	DefaultSkipThresholdPct int // 50-100 (§3.8)
	BoardRows               int
	BoardCols               int

	// SessionIDValue identifies the live game instance for persistence.
	SessionIDValue string
}

// SessionID returns the configured game session identifier.
func (c *Config) SessionID() string { return c.SessionIDValue }

func Load() *Config {
	return &Config{
		ListenAddr:              env("YFI_LISTEN_ADDR", ":4433"),
		HTTPAddr:                env("YFI_HTTP_ADDR", ":8777"),
		CertFile:                env("YFI_CERT_FILE", "certs/cert.pem"),
		KeyFile:                 env("YFI_KEY_FILE", "certs/key.pem"),
		RedisAddr:               env("YFI_REDIS_ADDR", "localhost:6379"),
		PostgresDSN:             env("YFI_POSTGRES_DSN", "postgres://yfitops:yfitops@localhost:5432/yfitops?sslmode=disable"),
		SpotifyClientID:         env("SPOTIFY_CLIENT_ID", ""),
		SpotifyClientSecret:     env("SPOTIFY_CLIENT_SECRET", ""),
		SpotifyRedirectURI:      env("SPOTIFY_REDIRECT_URI", "http://localhost:8777/auth/spotify/callback"),
		LRCLIBBaseURL:           env("YFI_LRCLIB_URL", "https://lrclib.net"),
		AdminSecret:             env("ADMIN_SECRET", "changeme-admin"),
		NonceSecret:             env("YFI_NONCE_SECRET", "dev-nonce-secret"),
		JoinSecret:              env("YFI_JOIN_SECRET", "dev-join-secret"),
		DefaultSkipThresholdPct: envInt("YFI_SKIP_THRESHOLD", 50),
		BoardRows:               envInt("YFI_BOARD_ROWS", 5),
		BoardCols:               envInt("YFI_BOARD_COLS", 5),
		SessionIDValue:          env("YFI_SESSION_ID", "session"),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

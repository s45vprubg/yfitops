package game

import (
	"context"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// ports.go defines the interface SEAMS between the game engine and its
// dependencies. These are FIXED CONTRACTS so leaf implementations (store,
// spotify, lyrics, transport) can be built in parallel and integrate cleanly.
// The engine depends only on these interfaces, never on concrete impls.

// BuzzLock is the atomic single-winner buzzer primitive (design_doc §3.4).
// Backed by Redis SET NX in production; an in-memory impl is used in tests.
// The FIRST caller to win a given (trackRound) key is the sole winner.
type BuzzLock interface {
	// TryAcquire atomically claims the buzzer for playerID on this round key.
	// Returns won=true for exactly one caller per key until Release.
	TryAcquire(ctx context.Context, roundKey, playerID string) (won bool, err error)
	// Release clears the lock so a new round can begin.
	Release(ctx context.Context, roundKey string) error
	// Holder returns the winning playerID, or "" if unclaimed.
	Holder(ctx context.Context, roundKey string) (string, error)
}

// GameRepo persists historical/relational data (design_doc §2 Postgres).
// Ephemeral live state lives in the engine/Redis; this is the audit + config
// layer (game sessions, players, tracks, leaderboard).
type GameRepo interface {
	CreateSession(ctx context.Context, s *Session) error
	SaveScore(ctx context.Context, sessionID, playerID, handle string, score int) error
	LogEvent(ctx context.Context, sessionID, kind string, detail map[string]any) error
	// LoadBoard returns the curated board (categories + track pools) for a game.
	LoadBoard(ctx context.Context, sessionID string) (*Board, error)
	// Leaderboard returns historical top scores.
	Leaderboard(ctx context.Context, limit int) ([]protocol.ScoreEntry, error)
}

// AudioDevice routes playback to the stage's Spotify Virtual Device
// (design_doc §6, §9). The Go backend never plays audio itself; it issues Web
// API commands to the stage's authenticated device, and for the latency-
// critical pause-on-buzz it instead messages the stage directly (handled in
// the engine, not here).
type AudioDevice interface {
	// SetDevice binds the stage's Spotify device ID after OAuth.
	SetDevice(deviceID string)
	Play(ctx context.Context, trackURI string, positionMs int64) error
	Pause(ctx context.Context) error
	Resume(ctx context.Context) error
	// AuthURL / Exchange support the admin OAuth flow on the stage tab.
	AuthURL(state string) string
	Exchange(ctx context.Context, code string) error
}

// LyricsProvider fetches synced lyrics (design_doc §6 LRCLIB).
type LyricsProvider interface {
	// Fetch returns time-coded lyric lines for a track, or nil if unavailable.
	Fetch(ctx context.Context, artist, title string, durationSec int) ([]protocol.LyricLine, error)
}

// InboundHandler is the transport layer's view of the engine. The transport
// owns connections and decodes frames; it forwards lifecycle + decoded client
// messages here. The engine runs a single command loop, so these calls must be
// safe to invoke from many connection goroutines (the engine serializes
// internally via a channel). This is the inbound seam, mirror of Broadcaster.
type InboundHandler interface {
	// OnConnect registers a new connection before any Hello is processed.
	// remoteIP is the client's network address (host only), used for anti-cheat
	// telemetry (shared-IP detection). Empty if unavailable.
	OnConnect(connID, remoteIP string)
	// OnMessage forwards a decoded client frame. role is the connection's
	// authenticated role (set during Hello); arrivalUnixMs is the SERVER
	// arrival clock used for buzz ordering (§4B) — transport stamps it the
	// instant the frame clears the network edge.
	OnMessage(connID string, role protocol.Role, env protocol.ClientEnvelope, arrivalUnixMs int64)
	// OnDisconnect removes a connection and recalculates active-user pools (§3.8).
	OnDisconnect(connID string)
}

// Broadcaster is the engine's view of the transport layer. The engine emits
// per-audience frames; the transport hub fans them out to connections,
// enforcing the sanitization rule (§4A) via the role argument.
type Broadcaster interface {
	// SendTo delivers a frame to a single connection.
	SendTo(connID string, env protocol.ServerEnvelope)
	// Broadcast delivers a frame to every connection of the given role.
	// Use this for role-scoped fan-out so mobile never receives reveal data.
	Broadcast(role protocol.Role, env protocol.ServerEnvelope)
	// BroadcastAll delivers to every connection regardless of role (use only
	// for already-sanitized payloads like StateData).
	BroadcastAll(env protocol.ServerEnvelope)
}

// Package protocol defines the wire contract between the Go backend and the
// three React clients (stage, mobile, admin) over WebTransport.
//
// This file is a FIXED CONTRACT. Every component — transport, game engine,
// and all three frontends — depends on these message shapes. Changing a field
// here ripples everywhere, so treat it as an API boundary, not scratch space.
//
// Design constraint (design_doc §4A — Client State Sanitization): messages sent
// to the *mobile* client must never carry track metadata (title, artist, URI,
// lyrics). Only the stage screen is trusted with reveal data. Enforcement lives
// in the game engine's per-audience serialization, but the types below are
// shaped to make that easy: ServerEnvelope.Payload is opaque per message type,
// and the sanitized mobile payloads are distinct structs.
package protocol

import "encoding/json"

// Protocol version. Bump on any breaking change to envelope shape.
const Version = 1

// ---------------------------------------------------------------------------
// Envelope
// ---------------------------------------------------------------------------

// Direction-agnostic envelope. Both client->server and server->client frames
// are JSON objects with a "t" (type) discriminator and a "d" (data) blob.
// Serialization is JSON for V2 (design_doc §11 leaves binary as a future
// optimization); the Type discriminator keeps the door open for protobuf later.

// ClientEnvelope is a frame sent from a client to the server.
type ClientEnvelope struct {
	Type ClientMsgType   `json:"t"`
	Data json.RawMessage `json:"d,omitempty"`
	// Nonce is the server-issued state nonce the client last observed
	// (design_doc §4D). Buzz/vote/admin actions carrying a stale nonce are
	// dropped. Zero means "unset / not required for this message".
	Nonce uint64 `json:"n,omitempty"`
}

// ServerEnvelope is a frame sent from the server to a client.
type ServerEnvelope struct {
	Type  ServerMsgType   `json:"t"`
	Data  json.RawMessage `json:"d,omitempty"`
	Nonce uint64          `json:"n,omitempty"`
	// Seq is a monotonically increasing per-connection sequence number so
	// clients can detect dropped/reordered frames over an unreliable stream.
	Seq uint64 `json:"s,omitempty"`
}

// ---------------------------------------------------------------------------
// Message types
// ---------------------------------------------------------------------------

type ClientMsgType string

const (
	// Common
	CMsgHello     ClientMsgType = "hello"     // {role, handle, deviceFP, joinToken, adminSecret?}
	CMsgHeartbeat ClientMsgType = "heartbeat" // {clientTime} -> RTT measurement (design_doc §4C)
	CMsgResync    ClientMsgType = "resync"    // request FULL_STATE_SYNC

	// Mobile player
	CMsgBuzz ClientMsgType = "buzz" // attempt the atomic buzzer lock
	CMsgVote ClientMsgType = "vote" // skip vote during karaoke (design_doc §3.8)
	CMsgRate ClientMsgType = "rate" // {stars 1-5} daily double crowd rating (§7)

	// Admin (require valid admin token)
	CMsgAdminGrade     ClientMsgType = "admin.grade"     // {verdict: correct|partial|incorrect}
	CMsgAdminSelect    ClientMsgType = "admin.select"    // {row,col} queue next cell
	CMsgAdminPlayback  ClientMsgType = "admin.playback"  // {action: play|pause|resume}
	CMsgAdminAward     ClientMsgType = "admin.award"     // {playerID, delta}
	CMsgAdminKick      ClientMsgType = "admin.kick"      // {playerID, ban bool}
	CMsgAdminReveal    ClientMsgType = "admin.reveal"    // force reveal current track
	CMsgAdminEndRound  ClientMsgType = "admin.endRound"  // force end current round
	CMsgAdminSetThresh ClientMsgType = "admin.setThresh" // {percent 50-100} skip threshold
	CMsgAdminEndGame   ClientMsgType = "admin.endGame"

	// Stage screen reports authoritative audio device events back (player_state_changed)
	CMsgStagePlayerState ClientMsgType = "stage.playerState" // {positionMs, paused, trackEnded}
	CMsgStageDeviceReady ClientMsgType = "stage.deviceReady" // {spotifyDeviceID}
)

type ServerMsgType string

const (
	SMsgWelcome   ServerMsgType = "welcome"   // {playerID, role, nonce}
	SMsgError     ServerMsgType = "error"     // {code, message}
	SMsgState     ServerMsgType = "state"     // STATE flag transition (sanitized) (design_doc §4A)
	SMsgFullSync  ServerMsgType = "fullSync"  // FULL_STATE_SYNC (audience-scoped)
	SMsgHeartbeat ServerMsgType = "heartbeat" // {serverTime, rttMs}

	// Mobile-facing (sanitized — NO track metadata)
	SMsgLockout    ServerMsgType = "lockout"    // {byHandle} buzzer locked by someone
	SMsgBuzzResult ServerMsgType = "buzzResult" // {won bool} did THIS client win the lock
	SMsgVoteState  ServerMsgType = "voteState"  // {have, need, voted}

	// Stage-facing (trusted — full reveal data)
	SMsgTrackStart ServerMsgType = "trackStart" // {maxPoints, basePoints, startTime, artistLen, songLen} (§5)
	SMsgReveal     ServerMsgType = "reveal"     // {artist, song, albumArt} full reveal
	SMsgLyrics     ServerMsgType = "lyrics"     // {lines: [{timeMs, text}]} LRCLIB synced (§6)
	SMsgScoreboard ServerMsgType = "scoreboard" // {players: [{id, handle, score}]}
	SMsgBoard      ServerMsgType = "board"      // grid state {cells: [{row,col,category,points,exhausted}]}

	// Audio routing command to the stage's Spotify Web Playback device (§9)
	SMsgAudio ServerMsgType = "audio" // {action: play|pause|resume, trackURI?, positionMs?}

	// Admin-facing (high-info telemetry)
	SMsgTelemetry ServerMsgType = "telemetry" // {connections:[{id,handle,rttMs,score,active}]}
	SMsgAdminView ServerMsgType = "adminView" // full evaluation context incl. correct answer
)

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

type Role string

const (
	RoleStage  Role = "stage"  // the central presentation screen (trusted)
	RoleMobile Role = "mobile" // attendee buzzer (untrusted, sanitized)
	RoleAdmin  Role = "admin"  // control room (trusted, token-gated)
)

// ---------------------------------------------------------------------------
// Client payloads
// ---------------------------------------------------------------------------

type HelloData struct {
	Role        Role   `json:"role"`
	Handle      string `json:"handle,omitempty"`
	DeviceFP    string `json:"deviceFP,omitempty"` // device fingerprint for ephemeral session resume (§3.2)
	JoinToken   string `json:"joinToken,omitempty"`
	AdminSecret string `json:"adminSecret,omitempty"`
}

type HeartbeatData struct {
	ClientTime int64 `json:"clientTime"` // client monotonic ms; used ONLY for RTT, never for ordering (§4B)
}

type GradeVerdict string

const (
	VerdictCorrect   GradeVerdict = "correct"
	VerdictPartial   GradeVerdict = "partial"
	VerdictIncorrect GradeVerdict = "incorrect"
)

type AdminGradeData struct {
	Verdict GradeVerdict `json:"verdict"`
	// PartialKind distinguishes which half was guessed, for the "remaining half"
	// scoring pool (§7). Only meaningful when Verdict == partial.
	PartialKind string `json:"partialKind,omitempty"` // "artist" | "song"
}

type AdminSelectData struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type AdminPlaybackData struct {
	Action string `json:"action"` // play | pause | resume
}

type AdminAwardData struct {
	PlayerID string `json:"playerID"`
	Delta    int    `json:"delta"`
}

type AdminKickData struct {
	PlayerID string `json:"playerID"`
	Ban      bool   `json:"ban"`
}

type AdminSetThreshData struct {
	Percent int `json:"percent"` // 50-100
}

type RateData struct {
	Stars int `json:"stars"` // 1-5
}

type StagePlayerStateData struct {
	PositionMs int64 `json:"positionMs"`
	Paused     bool  `json:"paused"`
	TrackEnded bool  `json:"trackEnded"`
}

type StageDeviceReadyData struct {
	SpotifyDeviceID string `json:"spotifyDeviceID"`
}

// ---------------------------------------------------------------------------
// Server payloads
// ---------------------------------------------------------------------------

type WelcomeData struct {
	PlayerID string `json:"playerID"`
	Role     Role   `json:"role"`
	Nonce    uint64 `json:"nonce"`
}

type ErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// StateData is the minimal control flag sent to ALL clients (design_doc §4A).
// It must never contain track metadata.
type StateData struct {
	State GameState `json:"state"`
}

type LockoutData struct {
	ByHandle string `json:"byHandle"`
}

type BuzzResultData struct {
	Won bool `json:"won"`
}

type VoteStateData struct {
	Have  int  `json:"have"`
	Need  int  `json:"need"`
	Voted bool `json:"voted"`
}

// TrackStartData drives the stage's deterministic point timer (design_doc §5).
// startTime is server epoch ms; the stage computes decay locally at 60fps.
type TrackStartData struct {
	MaxPoints  int   `json:"maxPoints"`
	BasePoints int   `json:"basePoints"`
	StartTime  int64 `json:"startTime"`
	ArtistLen  int   `json:"artistLen"` // for the masked-spaces decryption phase (§5)
	SongLen    int   `json:"songLen"`
}

type RevealData struct {
	Artist   string `json:"artist"`
	Song     string `json:"song"`
	AlbumArt string `json:"albumArt,omitempty"`
}

type LyricLine struct {
	TimeMs int64  `json:"timeMs"`
	Text   string `json:"text"`
}

type LyricsData struct {
	Lines []LyricLine `json:"lines"`
}

type ScoreEntry struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Score  int    `json:"score"`
}

type ScoreboardData struct {
	Players []ScoreEntry `json:"players"`
}

type BoardCell struct {
	Row        int    `json:"row"`
	Col        int    `json:"col"`
	Category   string `json:"category"`
	Points     int    `json:"points"`
	Exhausted  bool   `json:"exhausted"`
	TracksLeft int    `json:"tracksLeft"`
}

type BoardData struct {
	Rows  int         `json:"rows"`
	Cols  int         `json:"cols"`
	Cells []BoardCell `json:"cells"`
}

type AudioData struct {
	Action     string `json:"action"` // play | pause | resume
	TrackURI   string `json:"trackURI,omitempty"`
	PositionMs int64  `json:"positionMs,omitempty"`
}

type TelemetryConn struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	RTTMs  int    `json:"rttMs"`
	Score  int    `json:"score"`
	Active bool   `json:"active"`
}

type TelemetryData struct {
	Connections []TelemetryConn `json:"connections"`
}

// AdminViewData is the trusted evaluation context for the control room. It
// includes the correct answer so the host can grade. NEVER sent to mobile.
type AdminViewData struct {
	BuzzedPlayerID string `json:"buzzedPlayerID,omitempty"`
	BuzzedHandle   string `json:"buzzedHandle,omitempty"`
	CorrectArtist  string `json:"correctArtist,omitempty"`
	CorrectSong    string `json:"correctSong,omitempty"`
	CurrentPoints  int    `json:"currentPoints"`
}

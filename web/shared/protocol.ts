// protocol.ts — TypeScript mirror of server/internal/protocol/protocol.go.
// FIXED CONTRACT. Keep field names (the JSON tags) identical to the Go structs.
// The three frontends import these types; do not diverge from the Go side.

export const PROTOCOL_VERSION = 1;

export type Role = "stage" | "mobile" | "admin";

export type GameState =
  | "LOBBY"
  | "BOARD"
  | "ROUND_ACTIVE"
  | "LOCKED_OUT"
  | "ADJUDICATE"
  | "KARAOKE"
  | "DAILY_DOUBLE"
  | "TRANSITION"
  | "GAME_OVER";

// Client -> server message types
export type ClientMsgType =
  | "hello"
  | "heartbeat"
  | "resync"
  | "buzz"
  | "vote"
  | "rate"
  | "admin.grade"
  | "admin.select"
  | "admin.playback"
  | "admin.award"
  | "admin.kick"
  | "admin.reveal"
  | "admin.endRound"
  | "admin.setThresh"
  | "admin.endGame"
  // admin.setRevealCfg: tune the letter-reveal timing knobs live (applies next
  // round). Mirrors cmsgAdminSetRevealCfg in server reveal.go (CONTRACT-QUESTION).
  | "admin.setRevealCfg"
  | "stage.playerState"
  | "stage.deviceReady";

// Server -> client message types
export type ServerMsgType =
  | "welcome"
  | "error"
  | "state"
  | "fullSync"
  | "heartbeat"
  | "lockout"
  | "buzzResult"
  | "voteState"
  | "trackStart"
  | "reveal"
  | "lyrics"
  | "scoreboard"
  | "board"
  | "audio"
  | "telemetry"
  | "adminView"
  | "spotifyToken"
  // partialReveal: stage-only signal to fully reveal one field (artist|song)
  // after a partial guess. Mirrors smsgPartialReveal in server engine.go
  // (a CONTRACT-QUESTION type kept out of the fixed protocol.go).
  | "partialReveal"
  // maskedReveal: server-authoritative letter-by-letter decrypt frame, streamed
  // to BOTH stage and mobile in the same broadcast. Carries only already-
  // revealed letters, so a phone can never learn a letter before the projector
  // shows it (§4A extension). Mirrors smsgMaskedReveal in server reveal.go.
  | "maskedReveal"
  // adminRevealCfg: echoes current reveal-timing knob values to the control
  // room so its sliders reflect server truth. Mirrors smsgAdminRevealCfg.
  | "adminRevealCfg"
  // gradeResult: notifies mobile of a grade outcome (from ui-edits-3).
  | "gradeResult";

export interface ClientEnvelope<D = unknown> {
  t: ClientMsgType;
  d?: D;
  n?: number; // nonce echoed back (§4D)
}

export interface ServerEnvelope<D = unknown> {
  t: ServerMsgType;
  d?: D;
  n?: number;
  s?: number; // per-connection sequence
}

// ---- Client payloads ----
export interface HelloData {
  role: Role;
  handle?: string;
  deviceFP?: string;
  joinToken?: string;
  adminSecret?: string;
}
export interface HeartbeatData { clientTime: number; }
export type GradeVerdict = "correct" | "partial" | "incorrect";
export interface AdminGradeData { verdict: GradeVerdict; partialKind?: "artist" | "song"; }
export interface AdminSelectData { row: number; col: number; }
export interface AdminPlaybackData { action: "play" | "pause" | "resume"; }
export interface AdminAwardData { playerID: string; delta: number; }
export interface AdminKickData { playerID: string; ban: boolean; }
export interface AdminSetThreshData { percent: number; }
// Partial update of the reveal-timing knobs (only touched sliders sent).
export interface AdminSetRevealCfgData {
  intervalMs?: number;
  phase1Ms?: number;
  blockMs?: number;
  easeMs?: number;
  alternate?: boolean;
}
export interface RateData { stars: number; }
export interface StagePlayerStateData { positionMs: number; paused: boolean; trackEnded: boolean; }
export interface StageDeviceReadyData { spotifyDeviceID: string; }

// ---- Server payloads ----
export interface WelcomeData { playerID: string; role: Role; nonce: number; }
export interface ErrorData { code: string; message: string; }
export interface StateData { state: GameState; }
export interface LockoutData { byHandle: string; }
export interface BuzzResultData { won: boolean; }
export interface VoteStateData { have: number; need: number; voted: boolean; }
export interface TrackStartData {
  maxPoints: number;
  basePoints: number;
  startTime: number; // server epoch ms
  artistLen: number;
  songLen: number;
}
export interface RevealData { artist: string; song: string; albumArt?: string; }
// MaskedRevealData — the sanitized server-driven decrypt frame. Each mask array
// has length = field length; element is the revealed char, " " for a space, or
// "" for a not-yet-revealed slot (client renders local cosmetic noise there).
// During phase 1 (Noise) the arrays are a FIXED-WIDTH all-hidden block and the
// lengths are that block width — the real answer length is withheld until the
// block collapses to phase 2 (Skeleton). easeMs is how long the client should
// morph that collapse.
export interface MaskedRevealData {
  phase: 1 | 2 | 3 | 4;
  artistLen: number;
  songLen: number;
  artist: string[];
  song: string[];
  final?: boolean;
  easeMs?: number;
}
// Current reveal-timing knob values echoed to the control room.
export interface AdminRevealCfgData {
  intervalMs: number;
  phase1Ms: number;
  blockMs: number;
  easeMs: number;
  alternate: boolean;
}
export interface LyricLine { timeMs: number; text: string; }
export interface LyricsData { lines: LyricLine[]; }
export interface ScoreEntry { id: string; handle: string; score: number; }
export interface ScoreboardData { players: ScoreEntry[]; }
export interface BoardCell {
  row: number; col: number; category: string;
  points: number; exhausted: boolean; tracksLeft: number;
}
export interface BoardData { rows: number; cols: number; cells: BoardCell[]; }
export interface AudioData { action: "play" | "pause" | "resume"; trackURI?: string; positionMs?: number; }
export interface TelemetryConn { id: string; handle: string; rttMs: number; score: number; active: boolean; }
export interface TelemetryData { connections: TelemetryConn[]; }
export interface AdminViewData {
  buzzedPlayerID?: string;
  buzzedHandle?: string;
  correctArtist?: string;
  correctSong?: string;
  currentPoints: number;
}

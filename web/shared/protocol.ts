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
  | "adminView";

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

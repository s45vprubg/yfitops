// The audio layer is isolated behind this interface (design_doc §6, §9, BUILD
// CONTRACT honesty note). The Spotify Web Playback SDK cannot authenticate or
// play without a real Premium token, so the rest of the stage screen never
// depends on Spotify directly — it talks to an AudioPlayer. When no token is
// present we plug in a MockAudioPlayer and the whole show still runs in demo
// mode (animations, timer, board, lyrics rendering all work).

export type AudioMode = "spotify" | "demo";

export type ConnectState =
  | "idle" // demo mode, never tried Spotify
  | "connecting" // SDK loading / authenticating
  | "ready" // device registered, device_id known
  | "error"; // SDK or auth failed — fell back to demo

// Mirrors the data we report up via CMsgStagePlayerState {positionMs, paused, trackEnded}.
export interface PlayerState {
  positionMs: number;
  paused: boolean;
  trackEnded: boolean;
  // Wall-clock ms (performance.now-based epoch) at which this snapshot was
  // taken, so consumers can interpolate position between events (§6).
  sampledAt: number;
}

export interface AudioPlayer {
  readonly mode: AudioMode;

  /** Current connection lifecycle state, for the UI banner. */
  getConnectState(): ConnectState;

  /** Begin SDK init / OAuth. No-op for the mock. */
  connect(): Promise<void>;

  /** The Spotify Virtual Device id, once ready. */
  getDeviceId(): string | null;

  /** Unlock browser autoplay by calling activateElement(). Must be called from a user gesture. */
  activate(): Promise<void>;

  /** Apply a backend audio command (the ~20ms local pause-on-buzz path, §9). */
  play(trackURI?: string, positionMs?: number): Promise<void>;
  pause(): Promise<void>;
  resume(): Promise<void>;

  /** Latest known player state, interpolated to "now". */
  currentState(): PlayerState;

  /** Subscribe to player_state_changed-style updates. Returns unsubscribe. */
  onStateChange(cb: (s: PlayerState) => void): () => void;

  /** Fired once when the device becomes ready (device_id available). */
  onReady(cb: (deviceId: string) => void): () => void;

  destroy(): void;
}

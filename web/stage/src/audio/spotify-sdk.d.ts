// Minimal ambient typings for the Spotify Web Playback SDK
// (https://sdk.scdn.co/spotify-player.js). We only declare what we use.

interface SpotifyPlayerInit {
  name: string;
  getOAuthToken: (cb: (token: string) => void) => void;
  volume?: number;
}

interface SpotifyTrackWindow {
  current_track?: { name: string; uri: string };
}

interface SpotifyPlaybackState {
  paused: boolean;
  position: number;
  duration: number;
  track_window: SpotifyTrackWindow;
}

interface SpotifyWebPlaybackInstance {
  device_id: string;
}

interface SpotifyError {
  message: string;
}

interface SpotifyPlayer {
  connect(): Promise<boolean>;
  disconnect(): void;
  activateElement(): Promise<void>;
  pause(): Promise<void>;
  resume(): Promise<void>;
  togglePlay(): Promise<void>;
  seek(positionMs: number): Promise<void>;
  getCurrentState(): Promise<SpotifyPlaybackState | null>;
  setVolume(volume: number): Promise<void>;
  addListener(event: "ready" | "not_ready", cb: (i: SpotifyWebPlaybackInstance) => void): boolean;
  addListener(event: "player_state_changed", cb: (s: SpotifyPlaybackState | null) => void): boolean;
  addListener(
    event: "initialization_error" | "authentication_error" | "account_error" | "playback_error" | "autoplay_failed",
    cb: (e: SpotifyError) => void,
  ): boolean;
  removeListener(event: string): boolean;
}

interface SpotifyNamespace {
  Player: new (init: SpotifyPlayerInit) => SpotifyPlayer;
}

interface Window {
  Spotify?: SpotifyNamespace;
  onSpotifyWebPlaybackSDKReady?: () => void;
}

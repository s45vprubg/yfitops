// SpotifyAudioPlayer — wraps the Spotify Web Playback SDK so this browser tab
// becomes a "Virtual Device" (design_doc §6). It loads the SDK script, supplies
// tokens via the SDK's getOAuthToken callback, initializes a Player, and
// surfaces device readiness + player_state_changed events through the shared
// AudioPlayer interface.
//
// TOKEN LIFECYCLE: the constructor takes an ASYNC token provider. The SDK calls
// getOAuthToken every time it needs a token (including silently when the
// current one nears expiry), so the provider fetches a fresh token from the
// backend (/api/spotify/token), which refreshes server-side via the stored
// refresh token. This is what keeps audio alive through a multi-hour game —
// Spotify access tokens die ~1h after issue regardless of playback activity.
//
// HONESTY: this requires a real Spotify Premium account + valid token. If the
// token is missing or auth fails, callers should fall back to MockAudioPlayer.

import type { AudioPlayer, ConnectState, PlayerState } from "./types";

const SDK_SRC = "https://sdk.scdn.co/spotify-player.js";
const DEVICE_NAME = "yfitops Stage";

let sdkLoadPromise: Promise<void> | null = null;

function loadSdk(): Promise<void> {
  if (window.Spotify) return Promise.resolve();
  if (sdkLoadPromise) return sdkLoadPromise;

  sdkLoadPromise = new Promise<void>((resolve, reject) => {
    window.onSpotifyWebPlaybackSDKReady = () => resolve();
    const script = document.createElement("script");
    script.src = SDK_SRC;
    script.async = true;
    script.onerror = () => reject(new Error("failed to load Spotify SDK"));
    document.head.appendChild(script);
  });
  return sdkLoadPromise;
}

export class SpotifyAudioPlayer implements AudioPlayer {
  readonly mode = "spotify" as const;

  private player: SpotifyPlayer | null = null;
  private deviceId: string | null = null;
  private connectState: ConnectState = "idle";

  private last: PlayerState = { positionMs: 0, paused: true, trackEnded: false, sampledAt: performance.now() };

  private stateSubs = new Set<(s: PlayerState) => void>();
  private readySubs = new Set<(deviceId: string) => void>();

  // getToken returns a CURRENT access token, possibly async (it may hit the
  // backend token endpoint, which refreshes server-side). A plain string return
  // is also accepted for the simple/legacy case.
  constructor(private getToken: () => string | Promise<string>) {}

  getConnectState(): ConnectState {
    return this.connectState;
  }

  getDeviceId(): string | null {
    return this.deviceId;
  }

  async connect(): Promise<void> {
    this.connectState = "connecting";
    try {
      await loadSdk();
      if (!window.Spotify) throw new Error("Spotify SDK unavailable");

      const player = new window.Spotify.Player({
        name: DEVICE_NAME,
        getOAuthToken: (cb) => {
          // The SDK calls this whenever it needs a (fresh) token. Resolve the
          // provider — async-safe — so token refresh is transparent.
          Promise.resolve(this.getToken())
            .then((tok) => cb(tok))
            .catch((e) => console.warn("[spotify] token fetch failed:", e));
        },
        volume: 0.8,
      });
      this.player = player;

      player.addListener("ready", ({ device_id }) => {
        this.deviceId = device_id;
        this.connectState = "ready";
        this.readySubs.forEach((cb) => cb(device_id));
      });
      player.addListener("not_ready", () => {
        this.connectState = "error";
      });
      player.addListener("player_state_changed", (s) => this.ingest(s));

      const fail = (e: SpotifyError) => {
        this.connectState = "error";
        // eslint-disable-next-line no-console
        console.warn("[spotify]", e.message);
      };
      player.addListener("initialization_error", fail);
      player.addListener("authentication_error", fail);
      player.addListener("account_error", fail);
      player.addListener("playback_error", fail);

      const ok = await player.connect();
      if (!ok) this.connectState = "error";
    } catch (e) {
      this.connectState = "error";
      // eslint-disable-next-line no-console
      console.warn("[spotify] connect failed:", e);
    }
  }

  async play(_trackURI?: string, _positionMs?: number): Promise<void> {
    // Actual track routing happens server-side via the Spotify Web API targeting
    // this device_id. Locally we just ensure playback is resumed.
    await this.player?.resume().catch(() => {});
  }

  async pause(): Promise<void> {
    // The latency-critical path (§9): pause the local player immediately.
    await this.player?.pause().catch(() => {});
  }

  async resume(): Promise<void> {
    await this.player?.resume().catch(() => {});
  }

  currentState(): PlayerState {
    if (this.last.paused) return this.last;
    // Interpolate position forward from the last SDK snapshot (§6).
    const dt = performance.now() - this.last.sampledAt;
    return { ...this.last, positionMs: this.last.positionMs + dt };
  }

  onStateChange(cb: (s: PlayerState) => void): () => void {
    this.stateSubs.add(cb);
    return () => this.stateSubs.delete(cb);
  }

  onReady(cb: (deviceId: string) => void): () => void {
    this.readySubs.add(cb);
    if (this.deviceId) cb(this.deviceId);
    return () => this.readySubs.delete(cb);
  }

  destroy(): void {
    this.player?.disconnect();
    this.player = null;
    this.stateSubs.clear();
    this.readySubs.clear();
  }

  private ingest(s: SpotifyPlaybackState | null) {
    if (!s) {
      // A null state from the SDK usually means playback transferred away /
      // the device went idle — treat as track ended.
      this.last = { ...this.last, trackEnded: true, sampledAt: performance.now() };
    } else {
      this.last = {
        positionMs: s.position,
        paused: s.paused,
        trackEnded: s.paused && s.position === 0 && s.duration > 0,
        sampledAt: performance.now(),
      };
    }
    const snap = this.last;
    this.stateSubs.forEach((cb) => cb(snap));
  }
}

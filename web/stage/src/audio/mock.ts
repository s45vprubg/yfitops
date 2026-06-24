// MockAudioPlayer — used in demo mode when no Spotify token is available.
// It maintains a virtual playhead so the karaoke lyric highlighter still has a
// position to interpolate against, and so play/pause/resume visibly do
// something. No actual audio is produced.

import type { AudioPlayer, ConnectState, PlayerState } from "./types";

export class MockAudioPlayer implements AudioPlayer {
  readonly mode = "demo" as const;

  private paused = true;
  private basePositionMs = 0;
  private baseAt = performance.now();
  private stateSubs = new Set<(s: PlayerState) => void>();
  private tickHandle: number | null = null;

  getConnectState(): ConnectState {
    return "idle";
  }

  async connect(): Promise<void> {
    // No Spotify in demo mode.
  }

  getDeviceId(): string | null {
    return null;
  }

  async play(_trackURI?: string, positionMs = 0): Promise<void> {
    this.basePositionMs = positionMs;
    this.baseAt = performance.now();
    this.paused = false;
    this.startTicking();
    this.emit();
  }

  async pause(): Promise<void> {
    if (this.paused) return;
    this.basePositionMs = this.computePosition();
    this.baseAt = performance.now();
    this.paused = true;
    this.stopTicking();
    this.emit();
  }

  async resume(): Promise<void> {
    if (!this.paused) return;
    this.baseAt = performance.now();
    this.paused = false;
    this.startTicking();
    this.emit();
  }

  currentState(): PlayerState {
    return {
      positionMs: this.computePosition(),
      paused: this.paused,
      trackEnded: false,
      sampledAt: performance.now(),
    };
  }

  onStateChange(cb: (s: PlayerState) => void): () => void {
    this.stateSubs.add(cb);
    return () => this.stateSubs.delete(cb);
  }

  onReady(_cb: (deviceId: string) => void): () => void {
    // Mock never becomes a real device.
    return () => {};
  }

  destroy(): void {
    this.stopTicking();
    this.stateSubs.clear();
  }

  private computePosition(): number {
    if (this.paused) return this.basePositionMs;
    return this.basePositionMs + (performance.now() - this.baseAt);
  }

  private emit() {
    const s = this.currentState();
    this.stateSubs.forEach((cb) => cb(s));
  }

  // Emit a synthetic player_state_changed roughly every 250ms while playing so
  // the lyric highlighter behaves like it would with the real SDK.
  private startTicking() {
    if (this.tickHandle !== null) return;
    this.tickHandle = window.setInterval(() => this.emit(), 250);
  }

  private stopTicking() {
    if (this.tickHandle !== null) {
      window.clearInterval(this.tickHandle);
      this.tickHandle = null;
    }
  }
}

// useGame — the brain of the stage. Connects a GameClient as role "stage"
// (the TRUSTED client, §4A), subscribes to every server message the stage cares
// about, and exposes a consolidated, render-ready view of the game.
//
// It also owns the audio layer wiring (§6, §9):
//   - reports CMsgStageDeviceReady once the Spotify device registers
//   - applies SMsgAudio play/pause/resume to the local player (the ~20ms buzz path)
//   - reports CMsgStagePlayerState back on every player_state_changed
//
// Crucially it tracks the *clock skew* between the server's epoch (startTime in
// trackStart) and the local clock, so the deterministic point timer in the UI
// can compute elapsed = serverNow - startTime accurately. We approximate
// serverNow with Date.now(); the design accepts minor skew because the visual
// timer freezes on buzz to mask any discrepancy (§5).

import { useEffect, useRef, useState } from "react";
import { GameClient } from "@shared/client";
import type {
  AudioData,
  BoardData,
  GameState,
  LyricsData,
  MaskedRevealData,
  RevealData,
  ScoreboardData,
  ServerEnvelope,
  StateData,
  TrackStartData,
  WelcomeData,
} from "@shared/protocol";
import { WT_URL, STAGE_SECRET, fetchSpotifyToken } from "../config";
import { fetchCertHashes } from "./certHash";
import { createAudioPlayer, SpotifyAudioPlayer, type AudioPlayer, type ConnectState } from "../audio";

export interface TimerAnchor {
  // Row drives the decay ceiling/multiplier in scoring.currentPoints.
  row: number;
  maxPoints: number;
  basePoints: number;
  startTime: number; // server epoch ms
  // True while a buzz has frozen the timer (§5 latency masking).
  frozen: boolean;
}

export interface GameView {
  connected: boolean;
  state: GameState;
  board: BoardData | null;
  scoreboard: ScoreboardData | null;
  trackStart: TrackStartData | null;
  reveal: RevealData | null;
  maskedReveal: MaskedRevealData | null;
  revealedArtist: boolean;
  revealedSong: boolean;
  lyrics: LyricsData | null;
  // Lyric fetch lifecycle for the karaoke view: "idle" before a reveal,
  // "loading" while LRCLIB is being queried (show a spinner), "ready" once lines
  // arrived, "none" when the track has no synced lyrics.
  lyricsStatus: "idle" | "loading" | "ready" | "none";
  lockoutHandle: string | null;
  timer: TimerAnchor | null;
  animStartTime: number;
  audioMode: AudioPlayer["mode"];
  spotifyConnectState: ConnectState;
  audioActivated: boolean;
}

// Infer the board row for a given selection so the timer uses the right
// multiplier. trackStart carries maxPoints which encodes the row, but we map
// maxPoints -> row to feed scoring.currentPoints (which keys off row).
function rowFromMaxPoints(maxPoints: number): number {
  // maxPointsForRow: 100,125,150,175,200 for rows 1..5.
  const table = [100, 125, 150, 175, 200];
  const idx = table.indexOf(maxPoints);
  return idx >= 0 ? idx + 1 : 1;
}

export function useGame() {
  const [view, setView] = useState<GameView>({
    connected: false,
    state: "LOBBY",
    board: null,
    scoreboard: null,
    trackStart: null,
    reveal: null,
    maskedReveal: null,
    revealedArtist: false,
    revealedSong: false,
    lyrics: null,
    lyricsStatus: "idle",
    lockoutHandle: null,
    timer: null,
    animStartTime: 0,
    audioMode: "demo",
    spotifyConnectState: "idle",
    audioActivated: false,
  });

  const clientRef = useRef<GameClient | null>(null);
  const audioRef = useRef<AudioPlayer | null>(null);
  const spotifyInitedRef = useRef(false); // guard against double-init (push + full-sync)

  useEffect(() => {
    let disposed = false;
    const audio = createAudioPlayer();
    audioRef.current = audio;

    const patch = (p: Partial<GameView>) => setView((v) => ({ ...v, ...p }));
    patch({ audioMode: audio.mode, spotifyConnectState: audio.getConnectState() });

    (async () => {
      const serverCertHashes = await fetchCertHashes();
      if (disposed) return;

      const client = new GameClient({
        url: WT_URL,
        serverCertHashes,
        onState: (connected) => patch({ connected }),
      });
      clientRef.current = client;

      // ---- server -> stage subscriptions ----
      client.on("welcome", (e: ServerEnvelope) => {
        const _d = e.d as WelcomeData; // role/playerID/nonce; nonce tracked by client
        void _d;
      });

      client.on("state", (e: ServerEnvelope) => {
        const next = (e.d as StateData).state;
        setView((v) => {
          const out: Partial<GameView> = { state: next };
          // On a buzz the round freezes the visual timer instantly (§5).
          if (next === "LOCKED_OUT" && v.timer) {
            out.timer = { ...v.timer, frozen: true };
          }
          // Unfreeze when round resumes after an incorrect/partial grade.
          if (next === "ROUND_ACTIVE" && v.timer && v.timer.frozen) {
            out.timer = { ...v.timer, frozen: false };
          }
          // Leaving the active loop clears stale per-round data.
          if (next === "BOARD" || next === "LOBBY") {
            out.reveal = null;
            out.maskedReveal = null;
            out.revealedArtist = false;
            out.revealedSong = false;
            out.lyrics = null;
            out.lyricsStatus = "idle";
            out.lockoutHandle = null;
            out.trackStart = null;
            out.timer = null;
          }
          return { ...v, ...out };
        });
      });

      client.on("board", (e: ServerEnvelope) => patch({ board: e.d as BoardData }));
      client.on("scoreboard", (e: ServerEnvelope) => patch({ scoreboard: e.d as ScoreboardData }));

      client.on("trackStart", (e: ServerEnvelope) => {
        const ts = e.d as TrackStartData;
        const row = rowFromMaxPoints(ts.maxPoints);
        setView((v) => {
          // A truly new track changes the answer lengths; a mid-track re-anchor
          // (e.g. after a partial grade) only shifts the scoring pool/startTime.
          const isNewTrack = !v.trackStart ||
            v.trackStart.artistLen !== ts.artistLen ||
            v.trackStart.songLen !== ts.songLen;
          return {
            ...v,
            trackStart: ts,
            timer: { row, maxPoints: ts.maxPoints, basePoints: ts.basePoints, startTime: ts.startTime, frozen: false },
            lockoutHandle: null,
            ...(isNewTrack ? { animStartTime: ts.startTime, lyrics: null, lyricsStatus: "idle" as const, maskedReveal: null, revealedArtist: false, revealedSong: false } : {}),
          };
        });
      });

      client.on("reveal", (e: ServerEnvelope) => patch({ reveal: e.d as RevealData }));
      client.on("maskedReveal", (e: ServerEnvelope) => patch({ maskedReveal: e.d as MaskedRevealData }));
      client.on("partialReveal", (e: ServerEnvelope) => {
        const { field } = e.d as { field: string };
        if (field === "artist") patch({ revealedArtist: true });
        else if (field === "song") patch({ revealedSong: true });
      });
      client.on("lyrics", (e: ServerEnvelope) => patch({ lyrics: e.d as LyricsData, lyricsStatus: "ready" }));
      client.on("lyricsStatus", (e: ServerEnvelope) => {
        const s = (e.d as { status?: string })?.status;
        if (s === "loading" || s === "ready" || s === "none") patch({ lyricsStatus: s });
      });
      client.on("lockout", (e: ServerEnvelope) => {
        const handle = (e.d as { byHandle: string }).byHandle;
        setView((v) => ({ ...v, lockoutHandle: handle, timer: v.timer ? { ...v.timer, frozen: true } : v.timer }));
      });

      // ---- audio: backend commands -> local player (§9) ----
      client.on("audio", (e: ServerEnvelope) => {
        const a = e.d as AudioData;
        const player = audioRef.current;
        if (!player) return;
        if (a.action === "play") void player.play(a.trackURI, a.positionMs);
        else if (a.action === "pause") {
          void player.pause();
          setView((v) => ({ ...v, timer: v.timer ? { ...v.timer, frozen: true } : v.timer }));
        } else if (a.action === "resume") {
          void player.resume();
          setView((v) => ({ ...v, timer: v.timer ? { ...v.timer, frozen: false } : v.timer }));
        }
      });

      // initSpotify swaps in a Spotify-backed player. The token provider always
      // pulls a fresh token from the backend (/api/spotify/token, refreshed
      // server-side), falling back to a pushed token only if that fails.
      const initSpotify = (pushedToken: string) => {
        if (spotifyInitedRef.current) return; // idempotent — push + full-sync may both fire
        spotifyInitedRef.current = true;
        audioRef.current?.destroy();
        const spotify = new SpotifyAudioPlayer(async () => (await fetchSpotifyToken()) ?? pushedToken);
        audioRef.current = spotify;
        patch({ audioMode: "spotify", spotifyConnectState: "connecting" });
        spotify.onReady((deviceId) => {
          patch({ spotifyConnectState: "ready" });
          void client.send({ t: "stage.deviceReady", d: { spotifyDeviceID: deviceId } });
        });
        spotify.onStateChange((s) => {
          void client.send({
            t: "stage.playerState",
            d: { positionMs: Math.round(s.positionMs), paused: s.paused, trackEnded: s.trackEnded },
          });
        });
        // If the browser blocks playback (no user gesture yet), re-show the
        // activation overlay. Otherwise the stage sits silent with no sound and
        // no tab media indicator, and nobody knows why.
        spotify.onAutoplayBlocked?.(() => patch({ audioActivated: false }));
        void spotify.connect().then(() => {
          if (!disposed) patch({ spotifyConnectState: spotify.getConnectState() });
        });
      };

      // ---- spotifyToken: backend signals Spotify is authenticated. Fires both
      // when the admin completes OAuth AND on full-sync if a stage connects
      // afterward. The token may be empty (a "go fetch it" signal) — initSpotify
      // pulls the real token from the endpoint regardless.
      client.on("spotifyToken", (e: ServerEnvelope) => {
        const { token } = (e.d as { token?: string }) ?? {};
        initSpotify(token ?? "");
      });

      // ---- audio: local player -> backend reports ----
      audio.onReady((deviceId) => {
        patch({ spotifyConnectState: audio.getConnectState() });
        void client.send({ t: "stage.deviceReady", d: { spotifyDeviceID: deviceId } });
      });
      audio.onStateChange((s) => {
        void client.send({
          t: "stage.playerState",
          d: { positionMs: Math.round(s.positionMs), paused: s.paused, trackEnded: s.trackEnded },
        });
      });
      // Re-prompt for activation if this player (the initial createAudioPlayer
      // one) hits the browser autoplay block.
      audio.onAutoplayBlocked?.(() => patch({ audioActivated: false }));

      // If we have a Spotify token, kick off the SDK connect now.
      if (audio.mode === "spotify") {
        patch({ spotifyConnectState: "connecting" });
        await audio.connect();
        if (!disposed) patch({ spotifyConnectState: audio.getConnectState() });
      }

      // Send hello as the stage role (gated by the same secret as admin).
      try {
        await client.connect();
        await client.send({ t: "hello", d: { role: "stage", adminSecret: STAGE_SECRET } });
      } catch (err) {
        // eslint-disable-next-line no-console
        console.warn("[stage] connect failed (running offline/demo):", err);
        patch({ connected: false });
      }
    })();

    return () => {
      disposed = true;
      audioRef.current?.destroy();
      void clientRef.current?.close();
    };
  }, []);

  const activateAudio = async () => {
    const player = audioRef.current;
    if (!player) return;
    // Only dismiss the overlay if the element actually unlocked. If the browser
    // still rejected it, keep the prompt up so the operator can try again
    // rather than leaving the stage silently muted.
    const ok = await player.activate();
    if (ok) setView((v) => ({ ...v, audioActivated: true }));
  };

  return { view, audio: audioRef, activateAudio };
}

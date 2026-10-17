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
  DailyDoublePerformerData,
  DailyDoubleResultData,
  GameState,
  LyricsData,
  MaskedRevealData,
  RevealData,
  ScoreboardData,
  ServerEnvelope,
  SoloKaraokeActiveData,
  SoloKaraokeLyricsStatusData,
  SoloKaraokeNowPlayingData,
  StateData,
  TrackStartData,
  WelcomeData,
} from "@shared/protocol";
import type { ClientEnvelope } from "@shared/protocol";
import { WT_URL, fetchSpotifyToken } from "../config";
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

// Login/auth lifecycle, mirroring web/admin's useAdmin.ts ConnStatus shape.
// "idle" before any login attempt, "connecting" while the hello round-trip is
// in flight, "authed" once the server's welcome arrives, "error" on a
// forbidden/banned rejection (the secret was wrong).
export type AuthStatus = "idle" | "connecting" | "authed" | "error";

export interface GameView {
  connected: boolean;
  authStatus: AuthStatus;
  authError?: string;
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
  // The actual winner of the current round for the karaoke banner. undefined
  // until the server says (at karaoke); "" means nobody won.
  roundWinner: string | null;
  timer: TimerAnchor | null;
  animStartTime: number;
  audioMode: AudioPlayer["mode"];
  spotifyConnectState: ConnectState;
  audioActivated: boolean;
  // Sanitized (handle/ID only, no track data) — who's up for a Daily Double
  // and whether the performance has started. Null when not in one.
  ddPerformer: DailyDoublePerformerData | null;
  // The final average rating + points once a Daily Double resolves. Persists
  // through the transition back to BOARD so the reveal banner is visible;
  // cleared when the next Daily Double's offer starts or a new track begins.
  ddResult: DailyDoubleResultData | null;
  // Solo Karaoke (independent mode, orthogonal to GameState — the trivia
  // state machine stays pinned on LOBBY/GAME_OVER while this is true, per the
  // server's mode-lock). `soloKaraokeActive` is the broadcast every role gets
  // (§4A-safe boolean, mirrored to mobile's idle screen); the top-level App
  // uses it to switch into SoloKaraokeView ahead of the normal GameState
  // switch. `soloKaraokeNowPlaying` carries full track metadata — stage is
  // TRUSTED, so no sanitization applies here (unlike mobile).
  soloKaraokeActive: boolean;
  soloKaraokeNowPlaying: SoloKaraokeNowPlayingData | null;
  // Kept as its own field, distinct from `lyricsStatus` above — the server
  // deliberately broadcasts a separate soloKaraokeLyricsStatus message so the
  // two modes' lyric-fetch lifecycles never cross-contaminate each other.
  soloKaraokeLyricsStatus: "idle" | "loading" | "ready" | "none";
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

// useGame no longer auto-connects on mount (that's what let any visitor who
// merely loaded the stage page auto-authenticate with a build-time-embedded
// secret). It stays idle until login(secret) is called from the Login
// screen; each call is a fresh connect attempt, keyed by a nonce so retrying
// with the same (still-wrong) secret after fixing something else still
// re-triggers the effect below instead of silently no-op'ing on an unchanged
// dependency value.
export function useGame() {
  const [authAttempt, setAuthAttempt] = useState<{ secret: string; nonce: number } | null>(null);
  const [view, setView] = useState<GameView>({
    connected: false,
    authStatus: "idle",
    authError: undefined,
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
    roundWinner: null,
    timer: null,
    animStartTime: 0,
    audioMode: "demo",
    spotifyConnectState: "idle",
    audioActivated: false,
    ddPerformer: null,
    ddResult: null,
    soloKaraokeActive: false,
    soloKaraokeNowPlaying: null,
    soloKaraokeLyricsStatus: "idle",
  });

  const clientRef = useRef<GameClient | null>(null);
  const audioRef = useRef<AudioPlayer | null>(null);
  const spotifyInitedRef = useRef(false); // guard against double-init (push + full-sync)
  const connectedRef = useRef(false); // live link state, for guarding audio sends

  useEffect(() => {
    if (!authAttempt) return; // idle — no login attempt yet, nothing privileged runs
    const secret = authAttempt.secret;

    let disposed = false;
    // audioStarted guards startAudio() below against double-firing: "welcome"
    // re-fires on every reconnect (uistage-2's backoff re-sends hello), but the
    // audio session must only ever be built/connected once per login attempt.
    let audioStarted = false;

    const patch = (p: Partial<GameView>) => setView((v) => ({ ...v, ...p }));
    patch({ authStatus: "connecting", authError: undefined });

    // Send only when the current client is actually connected. Audio callbacks
    // fire on their own cadence (player_state_changed etc.); after a transport
    // drop an un-guarded client.send() throws "not connected", spamming
    // unhandled rejections (uistage-5). Guard on state and swallow races.
    const safeSend = (env: ClientEnvelope) => {
      const c = clientRef.current;
      if (!c || !connectedRef.current) return;
      void c.send(env).catch(() => {
        /* transport dropped mid-send — reconnect logic will recover */
      });
    };

    // ---- reconnect-with-backoff (uistage-2) ----
    // A WebTransport drop previously just flipped connected=false and wedged the
    // projector until a manual reload. We now re-run the full connect sequence
    // (fetch cert hashes -> new GameClient -> re-wire handlers -> connect ->
    // re-send hello) with capped exponential backoff. The audio layer is created
    // once, on the first "welcome" (see startAudio below), and is NOT torn down
    // across reconnects.
    let attempt = 0; // backoff step; reset to 0 on a good link
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let establishing = false; // guard against overlapping connect attempts

    const scheduleReconnect = () => {
      if (disposed) return;
      if (reconnectTimer !== null || establishing) return; // one in flight is enough
      const delay = Math.min(500 * 2 ** attempt, 10_000);
      attempt++;
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null;
        void establish();
      }, delay);
    };

    const onState = (connected: boolean) => {
      connectedRef.current = connected;
      patch({ connected });
      if (connected) {
        attempt = 0; // healthy link — reset the backoff ladder
      } else if (!disposed) {
        scheduleReconnect();
      }
    };

    // ---- audio: build + connect only AFTER the server confirms the secret ----
    // uis7-001: createAudioPlayer()/connect() previously ran unconditionally at
    // the top of this effect, racing the hello/welcome round trip that actually
    // validates `secret` server-side — a wrong secret could still register a
    // real Spotify Connect device off a stale cached token (see audio/index.ts
    // for the matching fix to liveTokenProvider). startAudio() is now only
    // invoked from the "welcome" handler below, strictly after authStatus flips
    // to "authed".
    const startAudio = () => {
      if (audioStarted) return;
      audioStarted = true;

      const player = createAudioPlayer(secret);
      audioRef.current = player;
      patch({ audioMode: player.mode, spotifyConnectState: player.getConnectState() });

      // Registered once against the authed-time player; sends route through
      // safeSend so a mid-drop callback no longer throws "not connected"
      // (uistage-5). initSpotify's replacement player wires its own callbacks.
      player.onReady((deviceId) => {
        patch({ spotifyConnectState: player.getConnectState() });
        safeSend({ t: "stage.deviceReady", d: { spotifyDeviceID: deviceId } });
        // Identity-checked for the same reason as initSpotify's site below
        // (s4-ui-new-04): this player is swapped out by initSpotify.
        void player.activate().then((ok) => { if (ok && !disposed && audioRef.current === player) patch({ audioActivated: true }); });
      });
      player.onStateChange((s) => {
        safeSend({
          t: "stage.playerState",
          d: { positionMs: Math.round(s.positionMs), paused: s.paused, trackEnded: s.trackEnded },
        });
      });
      // Re-prompt for activation if this player (the authed-time
      // createAudioPlayer one) hits the browser autoplay block.
      player.onAutoplayBlocked?.(() => patch({ audioActivated: false }));

      // If we have a Spotify token, kick off the real SDK connect now — only
      // reachable once authStatus === "authed" (see the "welcome" handler
      // immediately below).
      if (player.mode === "spotify") {
        patch({ spotifyConnectState: "connecting" });
        void player.connect().then(() => {
          if (!disposed) patch({ spotifyConnectState: player.getConnectState() });
        });
      }
    };

    // wireClientHandlers registers all server -> stage subscriptions on a client.
    // Called on every (re)connect so a fresh client re-receives full-sync state.
    const wireClientHandlers = (client: GameClient) => {
      // ---- server -> stage subscriptions ----
      client.on("welcome", (e: ServerEnvelope) => {
        const _d = e.d as WelcomeData; // role/playerID/nonce; nonce tracked by client
        void _d;
        patch({ authStatus: "authed" });
        // Gate the real Spotify SDK connect behind actual server-confirmed auth
        // (uis7-001) — not merely "a login attempt was submitted".
        startAudio();
      });

      // A bad secret surfaces here (server never sends welcome, just this).
      // Mirrors web/admin's useAdmin.ts error handling.
      client.on("error", (e: ServerEnvelope) => {
        const d = (e.d as { code?: string; message?: string }) ?? {};
        if (d.code === "forbidden" || d.code === "banned" || d.code === "unauthorized") {
          patch({ authStatus: "error", authError: `${d.code}: ${d.message ?? ""}`.trim() });
        }
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
            out.roundWinner = null;
            out.trackStart = null;
            out.timer = null;
            out.ddPerformer = null;
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
            // dd-ui-ms-3: cleared unconditionally on every trackStart, NOT
            // gated on isNewTrack's artistLen/songLen-equality heuristic. That
            // heuristic can rarely false-negative (two consecutive tracks
            // with identical artist+song string lengths), which would leave
            // a stale Daily Double result banner rendered over an unrelated
            // track. Any trackStart firing at all is a strong enough signal
            // that a prior Daily Double's result is stale, and pulling just
            // this field out of the isNewTrack gate doesn't touch the other
            // fields below (lyrics/maskedReveal/reveal flags/roundWinner),
            // which legitimately need to survive a mid-track re-anchor (e.g.
            // after a partial grade) and are left exactly as they were.
            ddResult: null,
            ...(isNewTrack ? { animStartTime: ts.startTime, lyrics: null, lyricsStatus: "idle" as const, maskedReveal: null, revealedArtist: false, revealedSong: false, roundWinner: null } : {}),
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
      client.on("roundWinner", (e: ServerEnvelope) => {
        const handle = (e.d as { handle?: string })?.handle ?? "";
        patch({ roundWinner: handle });
      });
      client.on("lockout", (e: ServerEnvelope) => {
        const handle = (e.d as { byHandle: string }).byHandle;
        setView((v) => ({ ...v, lockoutHandle: handle, timer: v.timer ? { ...v.timer, frozen: true } : v.timer }));
      });

      client.on("dailyDouble.performer", (e: ServerEnvelope) => {
        const d = e.d as DailyDoublePerformerData;
        setView((v) => ({
          ...v,
          ddPerformer: d,
          // A fresh (non-performing) broadcast means a NEW Daily Double is
          // starting — clear the previous one's result banner.
          ...(d.performing ? {} : { ddResult: null }),
        }));
      });
      client.on("dailyDouble.result", (e: ServerEnvelope) => patch({ ddResult: e.d as DailyDoubleResultData }));

      // ---- Solo Karaoke: independent mode, orthogonal to GameState ----
      client.on("soloKaraokeActive", (e: ServerEnvelope) => {
        patch({ soloKaraokeActive: (e.d as SoloKaraokeActiveData).active });
      });
      client.on("soloKaraokeNowPlaying", (e: ServerEnvelope) => {
        patch({ soloKaraokeNowPlaying: e.d as SoloKaraokeNowPlayingData });
      });
      client.on("soloKaraokeLyricsStatus", (e: ServerEnvelope) => {
        const s = (e.d as SoloKaraokeLyricsStatusData)?.status;
        if (s === "loading" || s === "ready" || s === "none") patch({ soloKaraokeLyricsStatus: s });
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

      // ---- spotifyToken: backend signals Spotify is authenticated. Fires both
      // when the admin completes OAuth AND on full-sync if a stage connects
      // afterward. The token may be empty (a "go fetch it" signal) — initSpotify
      // pulls the real token from the endpoint regardless.
      client.on("spotifyToken", (e: ServerEnvelope) => {
        const { token } = (e.d as { token?: string }) ?? {};
        initSpotify(token ?? "");
      });
    };

    // initSpotify swaps in a Spotify-backed player. The token provider always
    // pulls a fresh token from the backend (/api/spotify/token, refreshed
    // server-side), falling back to a pushed token only if that fails.
    // Reports go through safeSend so they survive a transport reconnect.
    const initSpotify = (pushedToken: string) => {
      if (spotifyInitedRef.current) return; // idempotent — push + full-sync may both fire
      spotifyInitedRef.current = true;
      audioRef.current?.destroy();
      // initSpotify only ever runs off a server-pushed "spotifyToken" message
      // (sent only to an already role-validated connection, per engine.go's
      // onHello secret check), so `pushedToken` is a trusted, backend-issued
      // value — not the attacker-reachable localStorage cache uis7-001
      // targets. A refresh failure here (auth OR network) safely falls back
      // to it unchanged.
      const spotify = new SpotifyAudioPlayer(async () => {
        const result = await fetchSpotifyToken(secret);
        return result.ok ? result.token : pushedToken;
      });
      audioRef.current = spotify;
      patch({ audioMode: "spotify", spotifyConnectState: "connecting" });
      spotify.onReady((deviceId) => {
        patch({ spotifyConnectState: "ready" });
        safeSend({ t: "stage.deviceReady", d: { spotifyDeviceID: deviceId } });
        // Auto-dismiss the activation overlay if the browser allows autoplay
        // (high MEI / localhost). Falls through silently if blocked.
        // Identity-checked (s4-ui-new-04): `disposed` only means the mount
        // effect is alive, not that this is still the LIVE player. initSpotify
        // destroys and replaces audioRef.current at runtime, so a slow
        // activate() from a dead player must not dismiss the overlay for a
        // successor that was never activated (silent projector, no re-arm).
        void spotify.activate().then((ok) => { if (ok && !disposed && audioRef.current === spotify) patch({ audioActivated: true }); });
      });
      spotify.onStateChange((s) => {
        safeSend({
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

    // establish (re)builds the transport: fresh cert hashes -> new client ->
    // re-wire handlers -> connect -> re-send hello. Reused by the initial
    // connect and every backoff retry. Overlap-guarded via `establishing`.
    const establish = async () => {
      if (disposed || establishing) return;
      establishing = true;
      try {
        const serverCertHashes = await fetchCertHashes();
        if (disposed) return;

        const client = new GameClient({
          url: WT_URL,
          serverCertHashes,
          // Ignore state events from a superseded client (s2-ui-01): a stale
          // client's late onState(false) (server closed the control stream but
          // the QUIC session lingers) must not tear down / reconnect on top of
          // a healthy newer client.
          onState: (connected) => {
            if (client !== clientRef.current) return;
            onState(connected);
          },
        });
        // Replacing the client: close the previous one first so its handleClose
        // no longer fires onState(false) (close() sets this.closed), preventing
        // a leaked client and duplicate reconnect churn (s2-ui-01).
        const prev = clientRef.current;
        clientRef.current = client;
        void prev?.close();
        wireClientHandlers(client);

        // Send hello as the stage role (gated by the same secret as admin,
        // entered at login time — never a build-time constant).
        await client.connect();
        await client.send({ t: "hello", d: { role: "stage", adminSecret: secret } });
      } catch (err) {
        // eslint-disable-next-line no-console
        console.warn("[stage] connect failed (running offline/demo):", err);
        connectedRef.current = false;
        patch({ connected: false });
        // connect() may reject before onState(false) ever fires — schedule the
        // retry ourselves so a failed initial/reconnect attempt still recovers.
        if (!disposed) {
          establishing = false;
          scheduleReconnect();
          return;
        }
      } finally {
        establishing = false;
      }
    };

    void establish();

    return () => {
      disposed = true;
      if (reconnectTimer !== null) {
        clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
      audioRef.current?.destroy();
      void clientRef.current?.close();
    };
  }, [authAttempt]);

  // login triggers a fresh connect attempt. Keyed by a nonce (not just the
  // secret string) so resubmitting the same still-wrong secret after fixing
  // something else still re-fires the effect above instead of being a no-op
  // against an unchanged dependency value.
  const login = (secret: string) => setAuthAttempt({ secret, nonce: Date.now() });

  const activateAudio = async () => {
    const player = audioRef.current;
    if (!player) return;
    // Only dismiss the overlay if the element actually unlocked. If the browser
    // still rejected it, keep the prompt up so the operator can try again
    // rather than leaving the stage silently muted.
    const ok = await player.activate();
    if (ok) setView((v) => ({ ...v, audioActivated: true }));
  };

  return { view, audio: audioRef, activateAudio, login };
}

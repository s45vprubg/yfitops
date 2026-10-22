import { useCallback, useEffect, useRef, useState } from "react";
import { GameClient } from "@shared/client";
import type {
  AdminAwardData,
  AdminGradeData,
  AdminKickData,
  AdminPlaybackData,
  AdminRevealCfgData,
  AdminSelectData,
  AdminSetRevealCfgData,
  AdminSetThreshData,
  AdminViewData,
  BoardData,
  ClientMsgType,
  DailyDoublePerformerData,
  DailyDoubleResultData,
  ErrorData,
  GameState,
  ModeLockData,
  PendingPickData,
  PickerStatusData,
  ScoreboardData,
  ServerEnvelope,
  SoloKaraokeErrorData,
  SoloKaraokeNowPlayingData,
  SoloKaraokePlayData,
  SoloKaraokeSeekData,
  SoloKaraokeSetRepeatData,
  SoloKaraokeSetShuffleData,
  TelemetryData,
  WelcomeData,
} from "@shared/protocol";
import { fetchCertHash, WT_URL } from "./config";

export type ConnStatus = "idle" | "connecting" | "authed" | "error" | "closed";

// Game states in which a track is actually loaded/playing (round/karaoke/daily
// double). Shared with GameControls.tsx (which uses it to gate Play/Pause and
// the Scrubber) so the two files can't drift on what counts as "playing" —
// used here to decide when to clear `playback` on a state transition (uia7-5).
export const PLAYING_STATES: GameState[] = [
  "ROUND_ACTIVE",
  "LOCKED_OUT",
  "KARAOKE",
  "DAILY_DOUBLE",
];

// Anti-cheat report (CONTRACT-QUESTION smsgCheatReport; not in shared protocol).
export interface CheatEntry {
  playerId: string;
  handle: string;
  ip: string;
  conns: number;
  flags: string[]; // "shared-ip", "multi-conn"
}
export interface CheatReport {
  players: CheatEntry[];
}

// Live playback position for the control-room scrubber (CONTRACT-QUESTION
// smsgPlaybackPosition; not in shared protocol). receivedAtMs is stamped
// client-side on arrival so the UI can interpolate smooth motion between
// snapshots the same way ActiveRound.tsx's point timer interpolates from a
// startTime anchor rather than storing a continuously-ticking number.
export interface PlaybackPosition {
  positionMs: number;
  durationMs: number;
  paused: boolean;
  receivedAtMs: number;
  hasDropPoint: boolean;
  dropPointMs?: number;
}

// The admin client holds NO authoritative state (§9). Everything below is a
// straight render of the most recent server payload. On reconnect the backend
// re-emits these via FULL_STATE_SYNC and the UI snaps back to the live state.
export interface AdminState {
  status: ConnStatus;
  error?: string;
  notice?: string; // transient operational message (e.g. "busy"); does NOT log out
  connected: boolean;
  playerID?: string;
  adminSecret?: string;
  gameState?: GameState;
  board?: BoardData;
  adminView?: AdminViewData;
  telemetry?: TelemetryData;
  scoreboard?: ScoreboardData;
  revealCfg?: AdminRevealCfgData; // current letter-reveal timing knobs
  cheat?: CheatReport; // anti-cheat signals (IP, shared-ip / multi-conn flags)
  playback?: PlaybackPosition; // live scrubber position/duration/paused
  // Daily Double status (sanitized: handle/ID only, no track data). Undefined
  // when not in one; performing=false means the contestant is still deciding
  // or picking a song (Force Skip applies), true means a track is playing
  // (the existing Force End Round finishes it).
  ddPerformer?: DailyDoublePerformerData;
  ddResult?: DailyDoubleResultData; // transient — last resolved payout
  // Who's designated to pick the next board cell, and whether they've
  // picked yet (sanitized: handle only, no board/track data). Broadcast to
  // all roles; the admin had no visibility into this before (s8uim-01) even
  // though admin.select is a real manual override for a stuck picker.
  pickerStatus?: PickerStatusData;
  // The cell the picker has queued for the next round (smsgPendingPick,
  // CONTRACT-QUESTION — see engine.go), sanitized to {row, col}. undefined/
  // row===0 means nothing is queued; the server re-broadcasts the cleared
  // zero value the instant the next round actually starts.
  pendingPick?: PendingPickData;
  // Operating playback volume (0-100), echoed from the server so the
  // slider reflects truth on connect/reconnect rather than assuming a
  // default. Also the level the Daily Double fade-out restores to.
  volumePct?: number;
  // Which mode currently owns playback — "none" | "trivia" | "solo_karaoke".
  // Every open /admin tab's Start Game button AND the Karaoke tab's Play
  // button must disable off this server broadcast, not local/per-tab state
  // (navigation/tab close is not a reliable release signal).
  modeLock?: ModeLockData["owner"];
  // Full Solo Karaoke now-playing metadata (admin isn't sanitization-gated).
  // index === -1 means nothing is loaded.
  soloKaraokeNowPlaying?: SoloKaraokeNowPlayingData;
  nonce: number;
}

export interface AdminActions {
  login: (secret: string) => Promise<void>;
  logout: () => void;
  clearNotice: () => void;
  select: (d: AdminSelectData) => void;
  grade: (d: AdminGradeData) => void;
  playback: (action: AdminPlaybackData["action"]) => void;
  seek: (positionMs: number) => void;
  markDropPoint: (positionMs: number) => void;
  jumpToDrop: () => void;
  setVolume: (percent: number) => void;
  endDailyDouble: () => void;
  award: (d: AdminAwardData) => void;
  kick: (d: AdminKickData) => void;
  reveal: () => void;
  endRound: () => void;
  skipDailyDouble: () => void;
  setThresh: (percent: number) => void;
  setRevealCfg: (cfg: AdminSetRevealCfgData) => void;
  endGame: () => void;
  // Solo Karaoke transport. setVolume above is reused unmodified (one
  // physical volume knob shared by both modes) — not duplicated here.
  soloKaraokePlay: (trackId?: string) => void;
  soloKaraokePause: () => void;
  soloKaraokeResume: () => void;
  soloKaraokeStop: () => void;
  soloKaraokeNext: () => void;
  soloKaraokePrev: () => void;
  soloKaraokeSeek: (positionMs: number) => void;
  soloKaraokeSetShuffle: (shuffle: boolean) => void;
  soloKaraokeSetRepeat: (repeat: SoloKaraokeSetRepeatData["repeat"]) => void;
}

const initial: AdminState = {
  status: "idle",
  connected: false,
  nonce: 0,
};

export function useAdmin(): [AdminState, AdminActions] {
  const [state, setState] = useState<AdminState>(initial);
  const clientRef = useRef<GameClient | null>(null);
  const secretRef = useRef<string>("");
  const patch = useCallback((p: Partial<AdminState>) => {
    setState((s) => ({ ...s, ...p }));
  }, []);

  // Tear down any live client on unmount.
  useEffect(() => {
    return () => {
      clientRef.current?.close().catch(() => {});
      clientRef.current = null;
    };
  }, []);

  const wire = useCallback(
    (client: GameClient) => {
      client.on("welcome", (env: ServerEnvelope) => {
        const d = env.d as WelcomeData;
        // Security (uiadmin-1): the admin secret is the crown-jewel trusted-role
        // credential (stage logs in with the same value, entered at runtime on
        // its own login screen). Keep it in-memory only — never persist to
        // localStorage where XSS could steal it or it could survive on a
        // shared/kiosk machine. An admin re-enters it after a page reload.
        patch({ status: "authed", playerID: d.playerID, error: undefined, adminSecret: secretRef.current });
      });
      client.on("error", (env: ServerEnvelope) => {
        const d = env.d as ErrorData;
        // Only auth failures should de-authenticate (bounce to the login
        // screen). Operational errors — e.g. "busy: round in progress",
        // "badCell" — are transient and must NOT log the admin out; surface
        // them as a dismissable notice while keeping the session.
        const isAuthError = d.code === "forbidden" || d.code === "banned" || d.code === "unauthorized";
        if (isAuthError) {
          patch({ status: "error", error: `${d.code}: ${d.message}` });
        } else {
          patch({ notice: `${d.code}: ${d.message}` });
        }
      });
      client.on("state", (env: ServerEnvelope) => {
        const state = (env.d as { state: GameState }).state;
        patch({
          gameState: state,
          ...(state === "DAILY_DOUBLE" ? {} : { ddPerformer: undefined }),
          // The DD result banner used to only clear when the NEXT Daily
          // Double started (ddPerformer going non-performing, above) — an
          // ordinary round in between left it stuck on screen through the
          // whole round. Clear it once a genuinely new round goes live;
          // finishDailyDouble() always routes through BOARD first, so the
          // result has already rendered by the time ROUND_ACTIVE is reachable.
          ...(state === "ROUND_ACTIVE" ? { ddResult: undefined } : {}),
          // uia7-5: a remounted Scrubber must not briefly render the PREVIOUS
          // track's stale duration/position/drop-point marker. Clear playback
          // whenever we leave a playing state; the next track's first
          // playbackPosition broadcast repopulates it.
          ...(PLAYING_STATES.includes(state) ? {} : { playback: undefined }),
        });
      });
      client.on("board", (env: ServerEnvelope) => {
        patch({ board: env.d as BoardData });
      });
      client.on("adminView", (env: ServerEnvelope) => {
        patch({ adminView: env.d as AdminViewData });
      });
      client.on("telemetry", (env: ServerEnvelope) => {
        patch({ telemetry: env.d as TelemetryData });
      });
      client.on("cheatReport", (env: ServerEnvelope) => {
        patch({ cheat: env.d as CheatReport });
      });
      client.on("playbackPosition", (env: ServerEnvelope) => {
        const d = env.d as Omit<PlaybackPosition, "receivedAtMs">;
        patch({ playback: { ...d, receivedAtMs: Date.now() } });
      });
      client.on("volumeStatus", (env: ServerEnvelope) => {
        patch({ volumePct: (env.d as { percent: number }).percent });
      });
      client.on("modeLock", (env: ServerEnvelope) => {
        patch({ modeLock: (env.d as ModeLockData).owner });
      });
      client.on("soloKaraokeNowPlaying", (env: ServerEnvelope) => {
        patch({ soloKaraokeNowPlaying: env.d as SoloKaraokeNowPlayingData });
      });
      // s10eng-07: a failed Play/Seek used to be logged server-side only,
      // with zero client-visible signal — surface it the same way an
      // operational "error" frame does (dismissable notice banner).
      client.on("soloKaraokeError", (env: ServerEnvelope) => {
        patch({ notice: `Solo Karaoke: ${(env.d as SoloKaraokeErrorData).message}` });
      });
      client.on("pickerStatus", (env: ServerEnvelope) => {
        patch({ pickerStatus: env.d as PickerStatusData });
      });
      client.on("pendingPick", (env: ServerEnvelope) => {
        const d = env.d as PendingPickData;
        patch({ pendingPick: d && d.row > 0 && d.col > 0 ? d : undefined });
      });
      client.on("scoreboard", (env: ServerEnvelope) => {
        patch({ scoreboard: env.d as ScoreboardData });
      });
      client.on("dailyDouble.performer", (env: ServerEnvelope) => {
        const d = env.d as DailyDoublePerformerData;
        // A fresh (non-performing) broadcast means a NEW Daily Double is
        // starting — clear the previous one's result.
        patch({ ddPerformer: d, ...(d.performing ? {} : { ddResult: undefined }) });
      });
      client.on("dailyDouble.result", (env: ServerEnvelope) => {
        patch({ ddResult: env.d as DailyDoubleResultData });
      });
      client.on("adminRevealCfg", (env: ServerEnvelope) => {
        patch({ revealCfg: env.d as AdminRevealCfgData });
      });
      // fullSync is a bundle; the backend follows it with the individual
      // payloads above, but accept a combined shape too for robustness.
      client.on("fullSync", (env: ServerEnvelope) => {
        const d = (env.d ?? {}) as Partial<{
          state: GameState;
          board: BoardData;
          adminView: AdminViewData;
          telemetry: TelemetryData;
          scoreboard: ScoreboardData;
          revealCfg: AdminRevealCfgData;
        }>;
        patch({
          ...(d.state ? { gameState: d.state } : {}),
          ...(d.board ? { board: d.board } : {}),
          ...(d.adminView ? { adminView: d.adminView } : {}),
          ...(d.telemetry ? { telemetry: d.telemetry } : {}),
          ...(d.scoreboard ? { scoreboard: d.scoreboard } : {}),
          ...(d.revealCfg ? { revealCfg: d.revealCfg } : {}),
        });
      });
      // Keep the nonce mirror fresh for the status display.
      client.onAny(() => patch({ nonce: client.nonce }));
    },
    [patch],
  );

  const login = useCallback(
    async (secret: string) => {
      // Capture+null the ref and patch to "connecting" BEFORE awaiting the
      // close of any prior connection (s5-ui-02). Login's busy gate is driven
      // by status === "connecting", so patching it synchronously here — before
      // anything yields — disables the form within this same click handler.
      // Previously the close() await came first, so on a re-login (where
      // close() stalls on a dead link) the button stayed enabled long enough
      // for an impatient second click to re-enter login() concurrently.
      const prev = clientRef.current;
      clientRef.current = null;
      secretRef.current = secret;
      patch({ status: "connecting", error: undefined });
      await prev?.close().catch(() => {});

      const certHashes = await fetchCertHash();
      const client = new GameClient({
        url: WT_URL,
        serverCertHashes: certHashes,
        onState: (connected) => {
          // Ignore state events from a superseded client (s5-ui-02): a stale
          // client's late onState must not corrupt the current connected/status
          // once a newer login() attempt has replaced it. Compares the locally
          // captured `client` const, not clientRef.current, mirroring the same
          // guard in mobile's useGame.ts and stage's useGame.ts.
          if (client !== clientRef.current) return;
          patch({ connected });
          setState((s) =>
            connected || s.status === "error"
              ? s
              : { ...s, status: "closed" },
          );
        },
      });
      clientRef.current = client;
      wire(client);

      try {
        await client.connect();
      } catch (e) {
        patch({
          status: "error",
          error: `connect failed: ${e instanceof Error ? e.message : String(e)}`,
        });
        return;
      }

      // Authenticate. The server replies with welcome (ok) or error/forbidden.
      await client.send({
        t: "hello",
        d: { role: "admin", adminSecret: secret },
      });
    },
    [patch, wire],
  );

  const logout = useCallback(() => {
    clientRef.current?.close().catch(() => {});
    clientRef.current = null;
    secretRef.current = "";
    setState(initial);
  }, []);

  const clearNotice = useCallback(() => patch({ notice: undefined }), [patch]);

  // Generic typed sender. GameClient stamps the latest nonce automatically.
  const sendAction = useCallback(<D>(t: ClientMsgType, d?: D) => {
    const c = clientRef.current;
    if (!c) return;
    c.send({ t, d }).catch(() => {});
  }, []);

  // Returning to a backgrounded tab never otherwise triggers a resync: a
  // live connection can sit through a one-shot broadcast while hidden/
  // throttled and never notice it missed anything. Admin has no
  // auto-reconnect (a drop requires manually re-entering the secret, by
  // design), so this only covers the still-connected case.
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === "visible") sendAction("resync");
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [sendAction]);

  const actions: AdminActions = {
    login,
    logout,
    clearNotice,
    select: (d) => sendAction<AdminSelectData>("admin.select", d),
    grade: (d) => sendAction<AdminGradeData>("admin.grade", d),
    playback: (action) =>
      sendAction<AdminPlaybackData>("admin.playback", { action }),
    seek: (positionMs) => sendAction<{ positionMs: number }>("admin.seek", { positionMs }),
    markDropPoint: (positionMs) =>
      sendAction<{ positionMs: number }>("admin.setDropPoint", { positionMs }),
    jumpToDrop: () => sendAction("admin.jumpToDropPoint"),
    setVolume: (percent) => sendAction<{ percent: number }>("admin.setVolume", { percent }),
    endDailyDouble: () => sendAction("admin.endDailyDouble"),
    award: (d) => sendAction<AdminAwardData>("admin.award", d),
    kick: (d) => sendAction<AdminKickData>("admin.kick", d),
    reveal: () => sendAction("admin.reveal"),
    endRound: () => sendAction("admin.endRound"),
    skipDailyDouble: () => sendAction("admin.skipDailyDouble"),
    setThresh: (percent) =>
      sendAction<AdminSetThreshData>("admin.setThresh", { percent }),
    setRevealCfg: (cfg) =>
      sendAction<AdminSetRevealCfgData>("admin.setRevealCfg", cfg),
    endGame: () => sendAction("admin.endGame"),
    // "" = play current/first (SoloKaraokePlayData.trackId; see protocol.ts).
    soloKaraokePlay: (trackId) =>
      sendAction<SoloKaraokePlayData>("admin.soloKaraoke.play", { trackId: trackId ?? "" }),
    soloKaraokePause: () => sendAction("admin.soloKaraoke.pause"),
    soloKaraokeResume: () => sendAction("admin.soloKaraoke.resume"),
    soloKaraokeStop: () => sendAction("admin.soloKaraoke.stop"),
    soloKaraokeNext: () => sendAction("admin.soloKaraoke.next"),
    soloKaraokePrev: () => sendAction("admin.soloKaraoke.prev"),
    soloKaraokeSeek: (positionMs) =>
      sendAction<SoloKaraokeSeekData>("admin.soloKaraoke.seek", { positionMs }),
    soloKaraokeSetShuffle: (shuffle) =>
      sendAction<SoloKaraokeSetShuffleData>("admin.soloKaraoke.setShuffle", { shuffle }),
    soloKaraokeSetRepeat: (repeat) =>
      sendAction<SoloKaraokeSetRepeatData>("admin.soloKaraoke.setRepeat", { repeat }),
  };

  return [state, actions];
}

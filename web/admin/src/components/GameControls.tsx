import { useEffect, useMemo, useRef, useState } from "react";
import type { GameState, ModeLockData } from "@shared/protocol";
import type { AdminActions, ConnStatus, PlaybackPosition } from "../useAdmin";
import { PLAYING_STATES } from "../useAdmin";
import { createAdminApi } from "../useAdminApi";
import StatusPill from "./StatusPill";
import { useModal } from "./Modal";

interface Props {
  status: ConnStatus;
  connected: boolean;
  nonce: number;
  gameState?: GameState;
  actions: AdminActions;
  adminSecret: string;
  spotifyConnected: boolean;
  playback?: PlaybackPosition;
  // "none" | "trivia" | "solo_karaoke" — Start Game is rejected server-side
  // (and disabled here) while Solo Karaoke owns the lock. Not local/per-tab
  // state: multiple admin tabs can be open at once and must stay in sync
  // purely off this broadcast.
  modeLock?: ModeLockData["owner"];
}

function formatMs(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${(s % 60).toString().padStart(2, "0")}`;
}

// Shared by the Scrubber's display AND "Mark Drop" (uia7-1) so the two can
// never drift apart again — both need the server's discrete positionMs
// snapshot projected forward to "now" and clamped to the track's bounds, since
// the server REJECTS (not clamps) an out-of-range admin.setDropPoint.
function interpolatePosition(playback: PlaybackPosition): number {
  const elapsed = playback.paused
    ? playback.positionMs
    : playback.positionMs + (Date.now() - playback.receivedAtMs);
  return Math.min(Math.max(elapsed, 0), playback.durationMs);
}

// Scrubber — click-to-seek playback bar. Interpolates smoothly between the
// server's discrete playbackPosition snapshots (positionMs + receivedAtMs)
// the same way ActiveRound.tsx's point timer interpolates from a startTime
// anchor via requestAnimationFrame, rather than storing a continuously
// ticking number in state. Exported: Solo Karaoke's Player reuses this
// unmodified (the same "playbackPosition" broadcast feeds both modes —
// engine.go's broadcastPlaybackPosition branches internally on
// soloKaraoke.active but emits the identical shape either way), wired to
// actions.soloKaraokeSeek instead of actions.seek.
export function Scrubber({
  playback,
  onSeek,
}: {
  playback?: PlaybackPosition;
  onSeek: (positionMs: number) => void;
}) {
  const barRef = useRef<HTMLDivElement>(null);
  const [, forceTick] = useState(0);
  // uia7-2: optimistic local seek target so the bar doesn't sit at the stale
  // pre-seek position until the next organic playbackPosition broadcast
  // arrives (which, on real Spotify hardware, is gated on Connect device-sync
  // latency, not an instant local event). Cleared once a real broadcast whose
  // receivedAtMs is after the click lands — the server's value then wins.
  const [optimisticSeek, setOptimisticSeek] = useState<{ positionMs: number; atMs: number } | null>(null);

  useEffect(() => {
    if (!playback || playback.paused) return;
    let raf: number;
    const loop = () => {
      forceTick((t) => t + 1);
      raf = requestAnimationFrame(loop);
    };
    raf = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(raf);
  }, [playback]);

  useEffect(() => {
    if (optimisticSeek && playback && playback.receivedAtMs >= optimisticSeek.atMs) {
      setOptimisticSeek(null);
    }
  }, [playback, optimisticSeek]);

  if (!playback || playback.durationMs <= 0) return null;

  const clamped = optimisticSeek
    ? Math.min(Math.max(optimisticSeek.positionMs, 0), playback.durationMs)
    : interpolatePosition(playback);
  const frac = clamped / playback.durationMs;

  return (
    <div
      ref={barRef}
      onClick={(e) => {
        const bar = barRef.current;
        if (!bar) return;
        const rect = bar.getBoundingClientRect();
        const clickFrac = Math.min(Math.max((e.clientX - rect.left) / rect.width, 0), 1);
        const positionMs = Math.round(clickFrac * playback.durationMs);
        setOptimisticSeek({ positionMs, atMs: Date.now() });
        onSeek(positionMs);
      }}
      title={`${formatMs(clamped)} / ${formatMs(playback.durationMs)}`}
      className="relative h-2 w-40 cursor-pointer overflow-hidden rounded-full border border-edge bg-panel"
    >
      <div className="absolute inset-y-0 left-0 bg-accent" style={{ width: `${frac * 100}%` }} />
      {playback.hasDropPoint && playback.dropPointMs !== undefined && (
        <div
          title={`Drop point: ${formatMs(playback.dropPointMs)}`}
          className="absolute inset-y-0 w-0.5 bg-amber-400"
          style={{ left: `${(playback.dropPointMs / playback.durationMs) * 100}%` }}
        />
      )}
    </div>
  );
}

const ACTIVE_GAME_STATES: GameState[] = [
  "BOARD",
  "ROUND_ACTIVE",
  "LOCKED_OUT",
  "ADJUDICATE",
  "KARAOKE",
  "DAILY_DOUBLE",
  "TRANSITION",
];

function isGameActive(s?: GameState): boolean {
  return !!s && ACTIVE_GAME_STATES.includes(s);
}

function isPlaying(s?: GameState): boolean {
  return !!s && PLAYING_STATES.includes(s);
}

export default function GameControls({
  status,
  connected,
  nonce,
  gameState,
  actions,
  adminSecret,
  spotifyConnected,
  playback,
  modeLock,
}: Props) {
  const { confirm } = useModal();
  const api = useMemo(() => createAdminApi(adminSecret), [adminSecret]);

  // s10ui1-02: the comment on these catch blocks ("state broadcast will
  // update UI") was false for every rejection — engine.go's StartGame()
  // returns its error BEFORE calling broadcastBoard/broadcastModeLock, so
  // no broadcast ever fires on a 409. A stale/not-yet-synced modeLock on
  // this tab (plausible: it's per-connection broadcast state, not
  // guaranteed synchronous across multiple open admin tabs) meant Start
  // Game could be clicked, get rejected, and show literally nothing.
  const [actionError, setActionError] = useState<string | null>(null);

  const handleStartGame = async () => {
    setActionError(null);
    try {
      await api.startGame();
    } catch (e) {
      setActionError(e instanceof Error ? e.message : "Failed to start game");
    }
  };

  const handleResetGame = async () => {
    setActionError(null);
    try {
      await api.resetGame();
    } catch (e) {
      setActionError(e instanceof Error ? e.message : "Failed to reset game");
    }
  };

  // uia7-3: in-flight guards against rapid double-click, mirroring
  // HoldingArea.tsx's rescanning/building/requeryingId boolean-guard pattern.
  // markDropPoint/jumpToDrop are fire-and-forget sendAction calls (no
  // await-able completion signal reaches this component), so the guard is
  // cleared on a short timeout fallback rather than on response receipt.
  const [markingDrop, setMarkingDrop] = useState(false);
  const [jumpingToDrop, setJumpingToDrop] = useState(false);
  const markingDropTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  const jumpingToDropTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    return () => {
      if (markingDropTimeout.current) clearTimeout(markingDropTimeout.current);
      if (jumpingToDropTimeout.current) clearTimeout(jumpingToDropTimeout.current);
    };
  }, []);

  const handleMarkDrop = () => {
    if (!playback || markingDrop) return;
    setMarkingDrop(true);
    // uia7-1: clamp with the SAME helper the Scrubber's display uses, so a
    // click near the end of the track can never send a positionMs past
    // durationMs and trip the server's dropPointOutOfRange rejection.
    actions.markDropPoint(interpolatePosition(playback));
    markingDropTimeout.current = setTimeout(() => setMarkingDrop(false), 1500);
  };

  const handleJumpToDrop = () => {
    if (jumpingToDrop) return;
    setJumpingToDrop(true);
    actions.jumpToDrop();
    jumpingToDropTimeout.current = setTimeout(() => setJumpingToDrop(false), 1500);
  };

  const gameActive = isGameActive(gameState);
  // A track only exists in the PLAYING_STATES (a round/karaoke is live). Between
  // rounds (BOARD/TRANSITION) there is nothing to play or pause, so BOTH
  // controls are disabled — previously Play looked enabled but the server
  // (correctly) no-oped it because there was no track loaded.
  const trackLoaded = isPlaying(gameState) && spotifyConnected;
  // Server truth (stage's own self-reported playback state), not local
  // optimism (§9: "the admin client holds NO authoritative state") — a
  // local-only toggle here used to always reset to "not paused" on every
  // mount, so an admin page reload showed Pause available even if the round
  // was genuinely still paused server-side. playback is re-sent immediately
  // on reconnect now (see sendFullSync's admin block in engine.go), so this
  // resyncs promptly instead of staying wrong until the next report.
  const isPaused = playback?.paused ?? false;
  const canPause = trackLoaded && !isPaused;
  const canResume = trackLoaded && isPaused;

  return (
    <header className="relative flex items-center gap-3 border-b border-edge bg-panel2 px-3 py-2">
      <div className="rounded border border-edge bg-panel px-2 py-0.5 font-mono text-[11px] uppercase tracking-wide text-slate-300">
        <span className="font-semibold text-white">{gameState ?? "—"}</span>
      </div>

      <div className="flex-1" />

      {/* Play/Pause toggle */}
      {canPause ? (
        <button
          onClick={() => actions.playback("pause")}
          className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-amber-300 hover:bg-amber-950/40"
        >
          Pause
        </button>
      ) : canResume ? (
        <button
          onClick={() => actions.playback("resume")}
          className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-emerald-300 hover:bg-emerald-950/40"
        >
          Resume
        </button>
      ) : (
        <button
          disabled
          className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-slate-500 disabled:pointer-events-none disabled:opacity-30"
        >
          Pause
        </button>
      )}

      {trackLoaded && <Scrubber playback={playback} onSeek={actions.seek} />}

      {trackLoaded && (
        <>
          <button
            onClick={handleMarkDrop}
            disabled={!playback || markingDrop}
            title="Mark the current position as this song's drop/chorus"
            className="rounded border border-edge bg-panel px-2 py-1.5 text-xs font-semibold text-slate-300 hover:bg-panel3 disabled:pointer-events-none disabled:opacity-30"
          >
            Mark Drop
          </button>
          <button
            onClick={handleJumpToDrop}
            disabled={!playback?.hasDropPoint || jumpingToDrop}
            title={playback?.hasDropPoint ? "Jump to the marked drop point" : "No drop point marked for this song yet"}
            className="rounded border border-amber-700 bg-amber-950/30 px-2 py-1.5 text-xs font-semibold text-amber-300 hover:bg-amber-900/40 disabled:pointer-events-none disabled:opacity-30"
          >
            Jump to Drop
          </button>
        </>
      )}

      {/* Single game action button: Start Game / End Game / New Game */}
      {gameState === "GAME_OVER" ? (
        <button
          onClick={handleResetGame}
          className="rounded border border-amber-600 bg-amber-950/50 px-3 py-1.5 text-xs font-bold uppercase text-amber-300 hover:bg-amber-900/60"
        >
          New Game
        </button>
      ) : gameActive ? (
        <button
          onClick={async () => {
            if (
              await confirm({
                title: "End game?",
                body: "End the entire game? This cannot be undone.",
                confirmLabel: "End Game",
                danger: true,
              })
            ) {
              actions.endGame();
            }
          }}
          className="rounded border border-red-800 bg-red-950/40 px-3 py-1.5 text-xs font-bold uppercase text-red-300 hover:bg-red-900/50"
        >
          End Game
        </button>
      ) : gameState === "LOBBY" ? (
        <button
          onClick={handleStartGame}
          disabled={!spotifyConnected || modeLock === "solo_karaoke"}
          title={
            modeLock === "solo_karaoke"
              ? "Solo Karaoke is active — stop it from the Karaoke tab first"
              : spotifyConnected
                ? undefined
                : "Connect Spotify before starting"
          }
          className="rounded border border-green-600 bg-green-950/50 px-3 py-1.5 text-xs font-bold uppercase text-green-300 hover:bg-green-900/60 disabled:pointer-events-none disabled:opacity-30"
        >
          Start Game
        </button>
      ) : null}

      <StatusPill status={status} connected={connected} nonce={nonce} />

      {actionError && (
        <div className="absolute left-0 top-full z-50 flex items-center gap-2 border-b border-red-900/60 bg-red-950/90 px-3 py-1.5 text-xs text-red-200 shadow-lg">
          <span>{actionError}</span>
          <button onClick={() => setActionError(null)} className="underline">Dismiss</button>
        </div>
      )}
    </header>
  );
}

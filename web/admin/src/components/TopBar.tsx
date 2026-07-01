import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { GameState } from "@shared/protocol";
import type { AdminActions, ConnStatus } from "../useAdmin";
import { createAdminApi, type BoardSummary } from "../useAdminApi";
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

const PLAYING_STATES: GameState[] = [
  "ROUND_ACTIVE",
  "LOCKED_OUT",
  "KARAOKE",
  "DAILY_DOUBLE",
];

function isGameActive(s?: GameState): boolean {
  return !!s && ACTIVE_GAME_STATES.includes(s);
}

function isPlaying(s?: GameState): boolean {
  return !!s && PLAYING_STATES.includes(s);
}

export default function TopBar({
  status,
  connected,
  nonce,
  gameState,
  actions,
  adminSecret,
  spotifyConnected,
}: Props) {
  const [boards, setBoards] = useState<BoardSummary[]>([]);
  const [loadingBoard, setLoadingBoard] = useState(false);
  const { confirm } = useModal();
  const api = useMemo(() => createAdminApi(adminSecret), [adminSecret]);

  const refreshBoards = useCallback(async () => {
    try {
      const list = await api.listBoards();
      setBoards(list ?? []);
    } catch { setBoards([]); }
  }, [api]);

  useEffect(() => { refreshBoards(); }, [refreshBoards]);

  const handleLoadBoard = async (boardId: string) => {
    if (!boardId) return;
    setLoadingBoard(true);
    try {
      await api.attachBoard(boardId, "session");
    } catch { /* engine broadcast will update UI */ }
    setLoadingBoard(false);
  };

  const handleStartGame = async () => {
    try {
      await api.startGame();
    } catch { /* state broadcast will update UI */ }
  };

  const handleResetGame = async () => {
    try {
      await api.resetGame();
    } catch { /* state broadcast will update UI */ }
  };

  const [manuallyPaused, setManuallyPaused] = useState(false);
  const prevState = useRef(gameState);
  useEffect(() => {
    if (prevState.current !== gameState) {
      setManuallyPaused(false);
      prevState.current = gameState;
    }
  }, [gameState]);

  const gameActive = isGameActive(gameState);
  // A track only exists in the PLAYING_STATES (a round/karaoke is live). Between
  // rounds (BOARD/TRANSITION) there is nothing to play or pause, so BOTH
  // controls are disabled — previously Play looked enabled but the server
  // (correctly) no-oped it because there was no track loaded.
  const trackLoaded = isPlaying(gameState) && spotifyConnected;
  const canPause = trackLoaded && !manuallyPaused;
  const canResume = trackLoaded && manuallyPaused;

  return (
    <header className="relative flex items-center gap-4 border-b border-edge bg-panel2 px-4 py-2.5">
      <div className="rounded border border-edge bg-panel px-2.5 py-1 font-mono text-xs uppercase tracking-wide text-slate-300">
        state: <span className="font-semibold text-white">{gameState ?? "—"}</span>
      </div>

      <div className="flex items-center gap-1">
        <select
          disabled={loadingBoard || boards.length === 0 || gameActive}
          onChange={(e) => handleLoadBoard(e.target.value)}
          defaultValue=""
          className="rounded border border-edge bg-panel px-2 py-1 text-xs text-slate-200 outline-none focus:border-accent disabled:opacity-40"
        >
          <option value="" disabled>Load board…</option>
          {boards.map((b) => (
            <option key={b.id} value={b.id}>{b.name}</option>
          ))}
        </select>
      </div>

      <div className="flex-1" />

      {/* Play/Pause toggle */}
      {canPause ? (
        <button
          onClick={() => { actions.playback("pause"); setManuallyPaused(true); }}
          className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-amber-300 hover:bg-amber-950/40"
        >
          Pause
        </button>
      ) : canResume ? (
        <button
          onClick={() => { actions.playback("resume"); setManuallyPaused(false); }}
          className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-emerald-300 hover:bg-emerald-950/40"
        >
          Play
        </button>
      ) : (
        <button
          disabled
          className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-slate-500 disabled:pointer-events-none disabled:opacity-30"
        >
          Pause
        </button>
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
          disabled={!spotifyConnected}
          title={spotifyConnected ? undefined : "Connect Spotify before starting"}
          className="rounded border border-green-600 bg-green-950/50 px-3 py-1.5 text-xs font-bold uppercase text-green-300 hover:bg-green-900/60 disabled:pointer-events-none disabled:opacity-30"
        >
          Start Game
        </button>
      ) : null}

      <StatusPill status={status} connected={connected} nonce={nonce} />
    </header>
  );
}

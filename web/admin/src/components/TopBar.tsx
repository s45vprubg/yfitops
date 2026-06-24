import { useCallback, useEffect, useMemo, useState } from "react";
import type { GameState } from "@shared/protocol";
import type { AdminActions, ConnStatus } from "../useAdmin";
import { createAdminApi, type BoardSummary } from "../useAdminApi";
import StatusPill from "./StatusPill";
import { HTTP_URL } from "../config";

interface Props {
  status: ConnStatus;
  connected: boolean;
  nonce: number;
  gameState?: GameState;
  actions: AdminActions;
  onLogout: () => void;
  adminSecret: string;
}

// Top bar: global controls (Volume best-effort, Pause/Resume, End Game) and the
// Skip Voting Threshold slider (50%-100%).
export default function TopBar({
  status,
  connected,
  nonce,
  gameState,
  actions,
  onLogout,
  adminSecret,
}: Props) {
  const [thresh, setThresh] = useState(50);
  const [volume, setVolume] = useState(100);
  const [boards, setBoards] = useState<BoardSummary[]>([]);
  const [loadingBoard, setLoadingBoard] = useState(false);
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

  // The slider is uncontrolled by the server (the server is authoritative for
  // the effective threshold, but it does not echo a threshold payload back in
  // the contract). Debounce sends on change.
  useEffect(() => {
    const id = setTimeout(() => actions.setThresh(thresh), 120);
    return () => clearTimeout(id);
  }, [thresh, actions]);

  return (
    <header className="flex items-center gap-4 border-b border-edge bg-panel2 px-4 py-2.5">
      <div className="flex items-baseline gap-2">
        <span className="text-sm font-bold tracking-[0.25em] text-accent">YFITOPS</span>
        <span className="text-xs uppercase tracking-wide text-slate-500">control room</span>
      </div>

      <div className="rounded border border-edge bg-panel px-2.5 py-1 font-mono text-xs uppercase tracking-wide text-slate-300">
        state: <span className="font-semibold text-white">{gameState ?? "—"}</span>
      </div>

      <div className="flex items-center gap-1">
        <select
          disabled={loadingBoard || boards.length === 0}
          onChange={(e) => handleLoadBoard(e.target.value)}
          defaultValue=""
          className="rounded border border-edge bg-panel px-2 py-1 text-xs text-slate-200 outline-none focus:border-accent"
        >
          <option value="" disabled>Load board…</option>
          {boards.map((b) => (
            <option key={b.id} value={b.id}>{b.name}</option>
          ))}
        </select>
        {gameState === "LOBBY" && (
          <button
            onClick={handleStartGame}
            className="rounded border border-green-600 bg-green-950/50 px-3 py-1 text-xs font-bold uppercase text-green-300 hover:bg-green-900/60"
          >
            Start Game
          </button>
        )}
      </div>

      <div className="flex-1" />

      {/* Volume — best-effort / optional. No protocol message exists for it, so
          this is a local UI affordance only and is purely cosmetic. */}
      <label className="flex items-center gap-2 text-xs text-slate-400">
        <span className="uppercase tracking-wide">Vol</span>
        <input
          type="range"
          min={0}
          max={100}
          value={volume}
          onChange={(e) => setVolume(Number(e.target.value))}
          className="w-24 accent-slate-400"
          title="Best-effort volume (UI only — no backend channel in protocol)"
        />
        <span className="w-8 font-mono text-slate-500">{volume}</span>
      </label>

      {/* Skip Voting Threshold slider 50%–100%. */}
      <label className="flex items-center gap-2 text-xs text-slate-300">
        <span className="uppercase tracking-wide">Skip thresh</span>
        <input
          type="range"
          min={0}
          max={100}
          value={thresh}
          onChange={(e) => setThresh(Number(e.target.value))}
          className="w-32 accent-accent"
        />
        <span className="w-10 font-mono font-semibold text-accent">{thresh}%</span>
      </label>

      <button
        onClick={() => window.open(`${HTTP_URL}/auth/spotify`, "_blank", "noopener")}
        className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-green-300 hover:bg-green-950/40"
      >
        Connect Spotify
      </button>
      <button
        onClick={() => actions.playback("pause")}
        className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-amber-300 hover:bg-amber-950/40"
      >
        Pause
      </button>
      <button
        onClick={() => actions.playback("resume")}
        className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-emerald-300 hover:bg-emerald-950/40"
      >
        Resume
      </button>
      <button
        onClick={() => {
          if (confirm("End the entire game? This cannot be undone.")) {
            actions.endGame();
          }
        }}
        className="rounded border border-red-800 bg-red-950/40 px-3 py-1.5 text-xs font-bold uppercase text-red-300 hover:bg-red-900/50"
      >
        End Game
      </button>

      <StatusPill status={status} connected={connected} nonce={nonce} />

      <button
        onClick={onLogout}
        className="rounded border border-edge bg-panel px-3 py-1.5 text-xs text-slate-400 hover:text-white"
      >
        Lock
      </button>
    </header>
  );
}

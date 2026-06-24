import { useEffect, useState } from "react";
import type { GameState } from "@shared/protocol";
import type { AdminActions, ConnStatus } from "../useAdmin";
import StatusPill from "./StatusPill";

interface Props {
  status: ConnStatus;
  connected: boolean;
  nonce: number;
  gameState?: GameState;
  actions: AdminActions;
  onLogout: () => void;
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
}: Props) {
  const [thresh, setThresh] = useState(50);
  const [volume, setVolume] = useState(100);

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
          min={50}
          max={100}
          value={thresh}
          onChange={(e) => setThresh(Number(e.target.value))}
          className="w-32 accent-accent"
        />
        <span className="w-10 font-mono font-semibold text-accent">{thresh}%</span>
      </label>

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

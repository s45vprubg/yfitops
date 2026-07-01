import { useEffect, useState } from "react";
import type { AdminRevealCfgData } from "@shared/protocol";
import type { AdminActions } from "../useAdmin";

// RevealSettingsPanel — a collapsible control-room panel for the letter-reveal
// timing knobs. Sliders/toggle are seeded from the server-echoed current values
// (state.revealCfg) so they reflect server truth on connect/reconnect. Changes
// are debounced and apply to the NEXT round (the in-flight reveal snapshots its
// timing at track start).

interface Props {
  cfg?: AdminRevealCfgData;
  actions: AdminActions;
  onClose: () => void;
}

// Match the server clamp bounds (reveal.go).
const INTERVAL_MIN = 250;
const INTERVAL_MAX = 10000;
const PHASE1_MIN = 0;
const PHASE1_MAX = 20000;

export default function RevealSettingsPanel({ cfg, actions, onClose }: Props) {
  const [intervalMs, setIntervalMs] = useState(cfg?.intervalMs ?? 2000);
  const [phase1Ms, setPhase1Ms] = useState(cfg?.phase1Ms ?? 5000);
  const [alternate, setAlternate] = useState(cfg?.alternate ?? true);
  // Track whether the user has edited, so a server echo doesn't clobber a value
  // mid-drag but a fresh (re)connect still seeds the controls.
  const [dirty, setDirty] = useState(false);

  // Seed from server values until the user starts editing.
  useEffect(() => {
    if (dirty || !cfg) return;
    setIntervalMs(cfg.intervalMs);
    setPhase1Ms(cfg.phase1Ms);
    setAlternate(cfg.alternate);
  }, [cfg, dirty]);

  // Debounced send of the numeric sliders.
  useEffect(() => {
    if (!dirty) return;
    const id = setTimeout(() => actions.setRevealCfg({ intervalMs, phase1Ms }), 150);
    return () => clearTimeout(id);
  }, [intervalMs, phase1Ms, dirty, actions]);

  return (
    <div className="absolute right-4 top-14 z-20 w-72 rounded-lg border border-edge bg-panel2 p-4 shadow-xl">
      <div className="mb-3 flex items-center justify-between">
        <span className="text-xs font-bold uppercase tracking-[0.2em] text-accent">Reveal settings</span>
        <button onClick={onClose} className="text-slate-500 hover:text-white" aria-label="Close">
          ✕
        </button>
      </div>

      <label className="mb-3 block text-xs text-slate-300">
        <div className="mb-1 flex justify-between">
          <span className="uppercase tracking-wide">Letter interval</span>
          <span className="font-mono font-semibold text-accent">{(intervalMs / 1000).toFixed(2)}s</span>
        </div>
        <input
          type="range"
          min={INTERVAL_MIN}
          max={INTERVAL_MAX}
          step={250}
          value={intervalMs}
          onChange={(e) => { setDirty(true); setIntervalMs(Number(e.target.value)); }}
          className="w-full accent-accent"
        />
      </label>

      <label className="mb-3 block text-xs text-slate-300">
        <div className="mb-1 flex justify-between">
          <span className="uppercase tracking-wide">Noise delay</span>
          <span className="font-mono font-semibold text-accent">{(phase1Ms / 1000).toFixed(1)}s</span>
        </div>
        <input
          type="range"
          min={PHASE1_MIN}
          max={PHASE1_MAX}
          step={500}
          value={phase1Ms}
          onChange={(e) => { setDirty(true); setPhase1Ms(Number(e.target.value)); }}
          className="w-full accent-accent"
        />
      </label>

      <label className="mb-3 flex items-center justify-between text-xs text-slate-300">
        <span className="uppercase tracking-wide">Alternate artist / song</span>
        <input
          type="checkbox"
          checked={alternate}
          onChange={(e) => {
            const v = e.target.checked;
            setDirty(true);
            setAlternate(v);
            actions.setRevealCfg({ alternate: v });
          }}
          className="h-4 w-4 accent-accent"
        />
      </label>

      <p className="text-[0.65rem] leading-snug text-slate-500">
        Changes apply to the next round; the round in progress keeps its pace.
      </p>
    </div>
  );
}

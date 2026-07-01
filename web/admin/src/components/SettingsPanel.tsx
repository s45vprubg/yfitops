import { useEffect, useRef, useState } from "react";
import type { AdminRevealCfgData } from "@shared/protocol";
import type { AdminActions } from "../useAdmin";

// SettingsPanel — the control-room "mixer": a single popover consolidating every
// live game knob (skip threshold + all reveal-timing knobs). Extensible: add a
// <Slider>/<Toggle> row per new knob. Reveal knobs seed from the server echo
// (state.revealCfg) and apply NEXT round; skip threshold applies immediately.

interface Props {
  revealCfg?: AdminRevealCfgData;
  actions: AdminActions;
  onClose: () => void;
}

// Server clamp bounds (mirror reveal.go).
const B = {
  interval: [250, 10000, 250] as const,
  phase1: [0, 20000, 500] as const,
  block: [0, 20000, 500] as const,
  ease: [0, 5000, 100] as const,
};

export default function SettingsPanel({ revealCfg, actions, onClose }: Props) {
  // Skip threshold has no server readback; keep local (seeded 50), like the old
  // inline slider. Applies immediately, debounced.
  const [thresh, setThresh] = useState(50);
  const threshTouched = useRef(false);
  useEffect(() => {
    if (!threshTouched.current) return;
    const id = setTimeout(() => actions.setThresh(thresh), 120);
    return () => clearTimeout(id);
  }, [thresh, actions]);

  // Reveal knobs seed from the server echo until the user edits.
  const [intervalMs, setIntervalMs] = useState(revealCfg?.intervalMs ?? 2000);
  const [phase1Ms, setPhase1Ms] = useState(revealCfg?.phase1Ms ?? 5000);
  const [blockMs, setBlockMs] = useState(revealCfg?.blockMs ?? 0);
  const [easeMs, setEaseMs] = useState(revealCfg?.easeMs ?? 600);
  const [alternate, setAlternate] = useState(revealCfg?.alternate ?? true);
  const revealTouched = useRef(false);

  useEffect(() => {
    if (revealTouched.current || !revealCfg) return;
    setIntervalMs(revealCfg.intervalMs);
    setPhase1Ms(revealCfg.phase1Ms);
    setBlockMs(revealCfg.blockMs);
    setEaseMs(revealCfg.easeMs);
    setAlternate(revealCfg.alternate);
  }, [revealCfg]);

  // Debounced send of the reveal sliders.
  useEffect(() => {
    if (!revealTouched.current) return;
    const id = setTimeout(
      () => actions.setRevealCfg({ intervalMs, phase1Ms, blockMs, easeMs }),
      150,
    );
    return () => clearTimeout(id);
  }, [intervalMs, phase1Ms, blockMs, easeMs, actions]);

  const editReveal = <T,>(setter: (v: T) => void) => (v: T) => {
    revealTouched.current = true;
    setter(v);
  };

  return (
    <div className="absolute right-4 top-14 z-20 w-80 rounded-lg border border-edge bg-panel2 p-4 shadow-xl">
      <div className="mb-3 flex items-center justify-between">
        <span className="text-xs font-bold uppercase tracking-[0.2em] text-accent">Settings</span>
        <button onClick={onClose} className="text-slate-500 hover:text-white" aria-label="Close">✕</button>
      </div>

      <Section title="Gameplay">
        <Slider
          label="Skip threshold"
          value={thresh}
          min={0}
          max={100}
          step={5}
          fmt={(v) => `${v}%`}
          onChange={(v) => { threshTouched.current = true; setThresh(v); }}
        />
      </Section>

      <Section title="Reveal timing" note="applies next round">
        <Slider label="Letter interval" value={intervalMs} min={B.interval[0]} max={B.interval[1]} step={B.interval[2]}
          fmt={(v) => `${(v / 1000).toFixed(2)}s`} onChange={editReveal(setIntervalMs)} />
        <Slider label="Letters start after" value={phase1Ms} min={B.phase1[0]} max={B.phase1[1]} step={B.phase1[2]}
          fmt={(v) => `${(v / 1000).toFixed(1)}s`} onChange={editReveal(setPhase1Ms)} />
        <Slider label="Hide length until" value={blockMs} min={B.block[0]} max={B.block[1]} step={B.block[2]}
          fmt={(v) => (v === 0 ? "off" : `${(v / 1000).toFixed(1)}s`)} onChange={editReveal(setBlockMs)} />
        <Slider label="Length morph" value={easeMs} min={B.ease[0]} max={B.ease[1]} step={B.ease[2]}
          fmt={(v) => `${(v / 1000).toFixed(1)}s`} onChange={editReveal(setEaseMs)} />
        <Toggle label="Alternate artist / song" checked={alternate}
          onChange={(v) => { revealTouched.current = true; setAlternate(v); actions.setRevealCfg({ alternate: v }); }} />
      </Section>
    </div>
  );
}

function Section({ title, note, children }: { title: string; note?: string; children: React.ReactNode }) {
  return (
    <div className="mb-4 last:mb-0">
      <div className="mb-2 flex items-baseline justify-between border-b border-edge/60 pb-1">
        <span className="text-[0.65rem] font-bold uppercase tracking-[0.2em] text-slate-400">{title}</span>
        {note && <span className="text-[0.6rem] italic text-slate-600">{note}</span>}
      </div>
      {children}
    </div>
  );
}

function Slider({
  label, value, min, max, step, fmt, onChange,
}: {
  label: string; value: number; min: number; max: number; step: number;
  fmt: (v: number) => string; onChange: (v: number) => void;
}) {
  return (
    <label className="mb-3 block text-xs text-slate-300 last:mb-0">
      <div className="mb-1 flex justify-between">
        <span className="uppercase tracking-wide">{label}</span>
        <span className="font-mono font-semibold text-accent">{fmt(value)}</span>
      </div>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="w-full accent-accent"
      />
    </label>
  );
}

function Toggle({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-center justify-between text-xs text-slate-300">
      <span className="uppercase tracking-wide">{label}</span>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className="h-4 w-4 accent-accent" />
    </label>
  );
}

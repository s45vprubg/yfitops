import { useEffect, useRef, useState } from "react";
import type { AdminRevealCfgData } from "@shared/protocol";
import type { AdminActions } from "../useAdmin";

// SettingsPage — the full-page "mixer" for every live game knob. Reached via the
// top-level Settings tab. Grouped into channels (Gameplay, Reveal timing) as
// vertical fader-style sliders; extensible — add a Knob to a Channel for any
// future setting. Reveal knobs seed from the server echo (state.revealCfg) and
// apply NEXT round; skip threshold applies immediately.

interface Props {
  revealCfg?: AdminRevealCfgData;
  actions: AdminActions;
}

// Server clamp bounds (mirror reveal.go).
const BOUNDS = {
  interval: [250, 10000, 250] as const,
  phase1: [0, 20000, 500] as const,
  block: [0, 20000, 500] as const,
  ease: [0, 5000, 100] as const,
};

export default function SettingsPage({ revealCfg, actions }: Props) {
  // Skip threshold has no server readback; local (seeded 50), applies immediately.
  const [thresh, setThresh] = useState(50);
  const threshTouched = useRef(false);
  useEffect(() => {
    if (!threshTouched.current) return;
    const id = setTimeout(() => actions.setThresh(thresh), 120);
    return () => clearTimeout(id);
  }, [thresh, actions]);

  // Reveal knobs seed from the server echo until edited.
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

  useEffect(() => {
    if (!revealTouched.current) return;
    const id = setTimeout(() => actions.setRevealCfg({ intervalMs, phase1Ms, blockMs, easeMs }), 150);
    return () => clearTimeout(id);
  }, [intervalMs, phase1Ms, blockMs, easeMs, actions]);

  const edit = <T,>(setter: (v: T) => void) => (v: T) => { revealTouched.current = true; setter(v); };

  return (
    <div className="h-full overflow-auto bg-[#05070a] p-6">
      <div className="mx-auto max-w-4xl">
        <h1 className="mb-1 text-lg font-bold uppercase tracking-[0.3em] text-accent">Settings</h1>
        <p className="mb-6 text-xs text-slate-500">Live game knobs. Reveal timing applies to the next round; skip threshold applies immediately.</p>

        <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
          <Channel title="Gameplay">
            <Fader label="Skip threshold" value={thresh} min={0} max={100} step={5}
              fmt={(v) => `${v}%`} onChange={(v) => { threshTouched.current = true; setThresh(v); }} />
          </Channel>

          <Channel title="Reveal timing" note="applies next round">
            <Fader label="Letter interval" value={intervalMs} min={BOUNDS.interval[0]} max={BOUNDS.interval[1]} step={BOUNDS.interval[2]}
              fmt={(v) => `${(v / 1000).toFixed(2)}s`} onChange={edit(setIntervalMs)} />
            <Fader label="Letters start after" value={phase1Ms} min={BOUNDS.phase1[0]} max={BOUNDS.phase1[1]} step={BOUNDS.phase1[2]}
              fmt={(v) => `${(v / 1000).toFixed(1)}s`} onChange={edit(setPhase1Ms)} />
            <Fader label="Hide length until" value={blockMs} min={BOUNDS.block[0]} max={BOUNDS.block[1]} step={BOUNDS.block[2]}
              fmt={(v) => (v === 0 ? "off" : `${(v / 1000).toFixed(1)}s`)} onChange={edit(setBlockMs)} />
            <Fader label="Length morph" value={easeMs} min={BOUNDS.ease[0]} max={BOUNDS.ease[1]} step={BOUNDS.ease[2]}
              fmt={(v) => `${(v / 1000).toFixed(1)}s`} onChange={edit(setEaseMs)} />
            <Switch label="Alternate artist / song" checked={alternate}
              onChange={(v) => { revealTouched.current = true; setAlternate(v); actions.setRevealCfg({ alternate: v }); }} />
          </Channel>
        </div>
      </div>
    </div>
  );
}

function Channel({ title, note, children }: { title: string; note?: string; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border border-edge bg-panel2 p-4">
      <div className="mb-4 flex items-baseline justify-between border-b border-edge/60 pb-2">
        <span className="text-xs font-bold uppercase tracking-[0.2em] text-slate-300">{title}</span>
        {note && <span className="text-[0.65rem] italic text-slate-600">{note}</span>}
      </div>
      <div className="flex flex-col gap-4">{children}</div>
    </section>
  );
}

function Fader({
  label, value, min, max, step, fmt, onChange,
}: {
  label: string; value: number; min: number; max: number; step: number;
  fmt: (v: number) => string; onChange: (v: number) => void;
}) {
  return (
    <label className="block text-sm text-slate-300">
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

function Switch({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-center justify-between text-sm text-slate-300">
      <span className="uppercase tracking-wide">{label}</span>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} className="h-5 w-5 accent-accent" />
    </label>
  );
}

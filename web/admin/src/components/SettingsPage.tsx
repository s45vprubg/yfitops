import { useEffect, useRef, useState } from "react";
import type { AdminRevealCfgData } from "@shared/protocol";
import type { AdminActions } from "../useAdmin";

// SettingsPage — a real mixing-desk: each knob is a VERTICAL fader (fills from
// the bottom like a channel strip) with the value on top and the label on the
// bottom cap, plus a toggle "switch" channel. Reveal knobs seed from the server
// echo and apply NEXT round; skip threshold applies immediately.

interface Props {
  revealCfg?: AdminRevealCfgData;
  actions: AdminActions;
}

// Server clamp bounds (mirror reveal.go): [min, max, step].
const B = {
  interval: [250, 10000, 250] as const,
  phase1: [0, 20000, 500] as const,
  block: [0, 20000, 500] as const,
  ease: [0, 5000, 100] as const,
};

export default function SettingsPage({ revealCfg, actions }: Props) {
  // Skip threshold: no server readback; local, applies immediately.
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
      <div className="mx-auto max-w-5xl">
        <h1 className="mb-1 text-lg font-bold uppercase tracking-[0.3em] text-accent">Mixer</h1>
        <p className="mb-6 text-xs text-slate-500">
          Live game knobs. Reveal channels apply to the next round; skip threshold applies immediately.
        </p>

        {/* The desk: channel strips on a dark rack. */}
        <div className="rounded-xl border border-edge bg-panel2 p-6 shadow-inner">
          <div className="flex flex-wrap items-end gap-3">
            <Fader label="Skip" sub="threshold" value={thresh} min={0} max={100} step={5}
              fmt={(v) => `${v}%`} onChange={(v) => { threshTouched.current = true; setThresh(v); }} />

            <Divider label="Reveal — next round" />

            <Fader label="Interval" sub="per letter" value={intervalMs} min={B.interval[0]} max={B.interval[1]} step={B.interval[2]}
              fmt={(v) => `${(v / 1000).toFixed(2)}s`} onChange={edit(setIntervalMs)} />
            <Fader label="Start" sub="letters after" value={phase1Ms} min={B.phase1[0]} max={B.phase1[1]} step={B.phase1[2]}
              fmt={(v) => `${(v / 1000).toFixed(1)}s`} onChange={edit(setPhase1Ms)} />
            <Fader label="Hide len" sub="block until" value={blockMs} min={B.block[0]} max={B.block[1]} step={B.block[2]}
              fmt={(v) => (v === 0 ? "off" : `${(v / 1000).toFixed(1)}s`)} onChange={edit(setBlockMs)} />
            <Fader label="Morph" sub="length ease" value={easeMs} min={B.ease[0]} max={B.ease[1]} step={B.ease[2]}
              fmt={(v) => `${(v / 1000).toFixed(1)}s`} onChange={edit(setEaseMs)} />

            <Switch label="Alt" sub="artist/song" checked={alternate}
              onChange={(v) => { revealTouched.current = true; setAlternate(v); actions.setRevealCfg({ alternate: v }); }} />
          </div>
        </div>
      </div>
    </div>
  );
}

// Divider — a labelled gap between channel groups on the desk.
function Divider({ label }: { label: string }) {
  return (
    <div className="mx-1 flex h-64 flex-col items-center justify-center">
      <div className="h-full w-px bg-edge" />
      <span className="mt-2 max-w-[3rem] rotate-180 text-center text-[9px] uppercase tracking-widest text-slate-600 [writing-mode:vertical-rl]">
        {label}
      </span>
    </div>
  );
}

// Fader — a vertical channel fader. The native range input is rotated so the
// thumb travels bottom→top like a real mixer slider; the filled track shows
// level. Value reads on top, label on the bottom cap.
function Fader({
  label, sub, value, min, max, step, fmt, onChange,
}: {
  label: string; sub?: string; value: number; min: number; max: number; step: number;
  fmt: (v: number) => string; onChange: (v: number) => void;
}) {
  const pct = max > min ? ((value - min) / (max - min)) * 100 : 0;
  return (
    <div className="flex w-16 flex-col items-center gap-2">
      <div className="font-mono text-xs font-semibold text-accent">{fmt(value)}</div>

      {/* Fader track: a tall rail with a fill from the bottom + the rotated input. */}
      <div className="relative flex h-56 w-8 items-center justify-center rounded bg-panel">
        <div className="absolute inset-x-0 bottom-0 rounded-b bg-accent/25" style={{ height: `${pct}%` }} />
        <div className="absolute left-1/2 top-0 h-full w-px -translate-x-1/2 bg-edge" />
        <input
          type="range"
          min={min}
          max={max}
          step={step}
          value={value}
          onChange={(e) => onChange(Number(e.target.value))}
          className="h-56 w-56 -rotate-90 cursor-pointer accent-accent"
          style={{ appearance: "auto" }}
        />
      </div>

      <div className="text-center leading-tight">
        <div className="text-[11px] font-bold uppercase tracking-wide text-slate-200">{label}</div>
        {sub && <div className="text-[9px] uppercase tracking-wide text-slate-500">{sub}</div>}
      </div>
    </div>
  );
}

// Switch — a mixer-style on/off channel (a vertical toggle button in the same
// footprint as a fader).
function Switch({ label, sub, checked, onChange }: { label: string; sub?: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex w-16 flex-col items-center gap-2">
      <div className={`font-mono text-xs font-semibold ${checked ? "text-accent" : "text-slate-600"}`}>
        {checked ? "ON" : "OFF"}
      </div>
      <button
        onClick={() => onChange(!checked)}
        className={`relative flex h-56 w-8 flex-col rounded p-1 transition ${
          checked ? "justify-start bg-accent/25" : "justify-end bg-panel"
        }`}
        aria-pressed={checked}
        title="Toggle"
      >
        <span className={`h-8 w-full rounded shadow ${checked ? "bg-accent" : "bg-slate-600"}`} />
      </button>
      <div className="text-center leading-tight">
        <div className="text-[11px] font-bold uppercase tracking-wide text-slate-200">{label}</div>
        {sub && <div className="text-[9px] uppercase tracking-wide text-slate-500">{sub}</div>}
      </div>
    </div>
  );
}

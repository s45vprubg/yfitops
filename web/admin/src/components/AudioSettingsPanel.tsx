import { useEffect, useRef, useState } from "react";
import type { AdminRevealCfgData } from "@shared/protocol";
import type { AdminActions } from "../useAdmin";

// AudioSettingsPanel — two independent sections stacked under the board:
//
// Volume: a live knob. Dragging it has an immediate, audible effect (and is
// what a Daily Double fade-out restores to), same as a real volume dial —
// debounced while dragging so a drag doesn't fire a Spotify API call per
// pixel, but otherwise applies as you go.
//
// Game Settings: everything else that used to live on the "Mixer" tab
// (letter-reveal timing, hint delays, skip threshold, reveal order). These
// are NOT a live audio mixer — they never were; that was a naming leftover
// from before this panel existed. Edits here are a pure draft: nothing is
// sent to the server until "Save Settings" is pressed, so idly adjusting a
// number can never change a setting mid-round by accident.

interface Props {
  volumePct?: number;
  onSetVolume: (percent: number) => void;
  revealCfg?: AdminRevealCfgData;
  actions: AdminActions;
}

// Server clamp bounds (mirror reveal.go), expressed in the unit the form
// actually shows: seconds for every *Ms knob (operators think in seconds,
// not milliseconds), raw counts for the two that aren't durations at all.
const SEC = {
  interval: [0.25, 10, 0.25] as const,
  phase1: [0, 20, 0.5] as const,
  block: [0, 20, 0.5] as const,
  ease: [0, 10, 0.1] as const,
  autoKaraoke: [0, 15, 0.5] as const,
  hintDelay: [0, 20, 0.5] as const,
  transitionDelay: [1, 10, 0.5] as const,
};
const LOCKOUT = [0, 20, 1] as const;
// Floor matches the server's own clamp (engine.go) and the original spec
// (design_doc.md: "Skip Voting Threshold slider (50% - 100%)") — below 50%
// a minority of voters could force a skip, so the server never allows it.
const THRESH = [50, 100, 5] as const;

export default function AudioSettingsPanel({ volumePct, onSetVolume, revealCfg, actions }: Props) {
  // ---- Volume: live, debounced only to coalesce drag ticks ----
  const [localVolume, setLocalVolume] = useState(volumePct ?? 100);
  const volumeTouched = useRef(false);
  useEffect(() => {
    if (volumeTouched.current) return;
    setLocalVolume(volumePct ?? 100);
  }, [volumePct]);
  useEffect(() => {
    if (!volumeTouched.current) return;
    const id = setTimeout(() => onSetVolume(localVolume), 120);
    return () => clearTimeout(id);
  }, [localVolume, onSetVolume]);

  // ---- Game Settings: draft-only until Save ----
  // threshTouched: skip-vote threshold has no server readback at all (never
  // echoed back by anything), so `thresh` is only ever a guess seeded at 50.
  // Save must NEVER push it unless the operator actually touched this one
  // control — otherwise saving an unrelated field (e.g. a reveal delay)
  // would silently clobber the real server-side threshold back to 50 (s9ui2-01).
  const threshTouched = useRef(false);
  const [thresh, setThresh] = useState(50);
  const [intervalSec, setIntervalSec] = useState(3);
  const [phase1Sec, setPhase1Sec] = useState(10);
  const [blockSec, setBlockSec] = useState(15);
  const [easeSec, setEaseSec] = useState(3);
  const [lockoutChars, setLockoutChars] = useState(2);
  const [autoKaraokeSec, setAutoKaraokeSec] = useState(3);
  const [yearDelaySec, setYearDelaySec] = useState(5);
  const [alternate, setAlternate] = useState(true);
  const [transitionDelaySec, setTransitionDelaySec] = useState(3);
  const [dirty, setDirty] = useState(false);
  const [saved, setSaved] = useState(false);

  // Mirrors the server's real defaults exactly (reveal.go's
  // defaultReveal*/defaultAutoKaraokeMs/defaultYearDelayMs
  // constants, engine.go's SkipThresholdPct==0 fallback) — "Reset to
  // Defaults" below restores these into the draft, not whatever the admin
  // last saved, same as a fresh game would start with.
  const DEFAULTS = {
    thresh: 50, intervalSec: 3, phase1Sec: 10, blockSec: 15, easeSec: 3,
    lockoutChars: 2, autoKaraokeSec: 3, yearDelaySec: 5,
    alternate: true, transitionDelaySec: 3,
  };

  // Seed the draft from the live server echo, but only while nothing is
  // being edited — the same "touched" guard the old sliders used, so a
  // broadcast landing mid-edit can never clobber what's being typed.
  //
  // seedSec guards against a field the server echo doesn't actually carry
  // (e.g. a newly-added knob like transitionDelayMs echoed by a backend
  // build that predates it): `undefined / 1000` is NaN, String(NaN) is
  // "NaN", and a type=number input renders a "NaN" value as BLANK — the
  // typed value visibly "disappearing" on Save even though the commit
  // itself succeeded. Falling back to the field's own current draft value
  // keeps the box showing something real instead of silently going blank.
  useEffect(() => {
    if (dirty || !revealCfg) return;
    const seedSec = (ms: number | undefined, fallbackSec: number) =>
      typeof ms === "number" && Number.isFinite(ms) ? ms / 1000 : fallbackSec;
    setIntervalSec(seedSec(revealCfg.intervalMs, intervalSec));
    setPhase1Sec(seedSec(revealCfg.phase1Ms, phase1Sec));
    setBlockSec(seedSec(revealCfg.blockMs, blockSec));
    setEaseSec(seedSec(revealCfg.easeMs, easeSec));
    setLockoutChars(
      typeof revealCfg.lockoutChars === "number" && Number.isFinite(revealCfg.lockoutChars)
        ? revealCfg.lockoutChars
        : lockoutChars,
    );
    setAutoKaraokeSec(seedSec(revealCfg.autoKaraokeMs, autoKaraokeSec));
    setYearDelaySec(seedSec(revealCfg.yearDelayMs, yearDelaySec));
    setAlternate(typeof revealCfg.alternate === "boolean" ? revealCfg.alternate : alternate);
    setTransitionDelaySec(seedSec(revealCfg.transitionDelayMs, transitionDelaySec));
  }, [revealCfg, dirty]);

  const edit = <T,>(setter: (v: T) => void) => (v: T) => {
    setDirty(true);
    setSaved(false);
    setter(v);
  };
  const editThresh = (v: number) => { threshTouched.current = true; edit(setThresh)(v); };

  const handleSave = () => {
    if (threshTouched.current) actions.setThresh(thresh);
    actions.setRevealCfg({
      intervalMs: Math.round(intervalSec * 1000),
      phase1Ms: Math.round(phase1Sec * 1000),
      blockMs: Math.round(blockSec * 1000),
      easeMs: Math.round(easeSec * 1000),
      lockoutChars: Math.round(lockoutChars),
      autoKaraokeMs: Math.round(autoKaraokeSec * 1000),
      yearDelayMs: Math.round(yearDelaySec * 1000),
      alternate,
      transitionDelayMs: Math.round(transitionDelaySec * 1000),
    });
    setDirty(false);
    setSaved(true);
  };

  // Resets the DRAFT to the real defaults — does NOT touch the server. Same
  // "nothing happens until Save" rule as every other edit: lets an operator
  // see exactly what they're about to apply, and back out via the X-equivalent
  // (just don't press Save) if a reset turns out to be the wrong call too.
  const handleResetDefaults = () => {
    threshTouched.current = true; // defaults include a real threshold value; must be saveable
    setThresh(DEFAULTS.thresh);
    setIntervalSec(DEFAULTS.intervalSec);
    setPhase1Sec(DEFAULTS.phase1Sec);
    setBlockSec(DEFAULTS.blockSec);
    setEaseSec(DEFAULTS.easeSec);
    setLockoutChars(DEFAULTS.lockoutChars);
    setAutoKaraokeSec(DEFAULTS.autoKaraokeSec);
    setYearDelaySec(DEFAULTS.yearDelaySec);
    setAlternate(DEFAULTS.alternate);
    setTransitionDelaySec(DEFAULTS.transitionDelaySec);
    setDirty(true);
    setSaved(false);
  };

  return (
    <div className="border-t border-edge">
      {/* Volume */}
      <div className="flex items-baseline justify-between px-3 py-2">
        <h2 className="text-xs font-bold uppercase tracking-[0.25em] text-accent">Volume</h2>
      </div>
      <div className="flex items-center gap-2 border-b border-edge px-3 py-2">
        <span className="text-[10px] uppercase tracking-wide text-slate-500" title="Playback volume">
          🔊
        </span>
        <input
          type="range"
          min={0}
          max={100}
          value={localVolume}
          onChange={(e) => { volumeTouched.current = true; setLocalVolume(Number(e.target.value)); }}
          className="flex-1 accent-accent"
          aria-label="Playback volume"
        />
        <span className="tnum w-8 text-right text-[10px] text-slate-500">{localVolume}%</span>
      </div>

      {/* Game Settings */}
      <div className="flex items-baseline justify-between px-3 py-2">
        <h2 className="text-xs font-bold uppercase tracking-[0.25em] text-accent">Game Settings</h2>
        {dirty && <span className="text-[10px] font-semibold uppercase text-amber-400">unsaved changes</span>}
      </div>
      <p className="px-3 pb-1 text-[10px] text-slate-500">
        A draft until you press Save — nothing here changes live while you type.
      </p>

      <div className="max-h-[40vh] overflow-auto px-3">
        <SettingRow label="Skip vote threshold" sub="% of players voting to skip a track"
          value={thresh} min={THRESH[0]} max={THRESH[1]} step={THRESH[2]} suffix="%"
          onChange={editThresh} />

        <Divider label="Between rounds — apply next round" />
        <SettingRow label="Transition delay" sub="seconds the board shows before the next track starts"
          value={transitionDelaySec} min={SEC.transitionDelay[0]} max={SEC.transitionDelay[1]} step={SEC.transitionDelay[2]} suffix="s"
          onChange={edit(setTransitionDelaySec)} />

        <Divider label="Hints — apply next round" />
        <SettingRow label="Reveal year after" sub="seconds into the track"
          value={yearDelaySec} min={SEC.hintDelay[0]} max={SEC.hintDelay[1]} step={SEC.hintDelay[2]} suffix="s"
          onChange={edit(setYearDelaySec)} />

        <Divider label="Letter reveal — apply next round" />
        <SettingRow label="Letter reveal interval" sub="seconds between each revealed letter"
          value={intervalSec} min={SEC.interval[0]} max={SEC.interval[1]} step={SEC.interval[2]} suffix="s"
          onChange={edit(setIntervalSec)} />
        <SettingRow label="Start revealing letters after" sub="seconds into the track"
          value={phase1Sec} min={SEC.phase1[0]} max={SEC.phase1[1]} step={SEC.phase1[2]} suffix="s"
          onChange={edit(setPhase1Sec)} />
        <SettingRow label="Hide title length until" sub="seconds (0 = never hide the length)"
          value={blockSec} min={SEC.block[0]} max={SEC.block[1]} step={SEC.block[2]} suffix="s"
          onChange={edit(setBlockSec)} />
        <SettingRow label="Length reveal animation" sub="seconds to morph to the real length"
          value={easeSec} min={SEC.ease[0]} max={SEC.ease[1]} step={SEC.ease[2]} suffix="s"
          onChange={edit(setEaseSec)} />
        <SettingRow label="Lock the buzzer with" sub="letters still unrevealed"
          value={lockoutChars} min={LOCKOUT[0]} max={LOCKOUT[1]} step={LOCKOUT[2]} suffix="chars"
          onChange={edit(setLockoutChars)} />
        <SettingRow label="Auto-start karaoke after" sub="seconds after reveal completes (0 = off)"
          value={autoKaraokeSec} min={SEC.autoKaraoke[0]} max={SEC.autoKaraoke[1]} step={SEC.autoKaraoke[2]} suffix="s"
          onChange={edit(setAutoKaraokeSec)} />

        <label className="flex items-center justify-between gap-3 border-b border-edge/50 py-2.5 text-xs last:border-0">
          <span className="text-slate-300">Alternate artist/song reveal order</span>
          <input
            type="checkbox"
            checked={alternate}
            onChange={(e) => edit(setAlternate)(e.target.checked)}
            className="h-4 w-4 accent-accent"
          />
        </label>
      </div>

      <div className="flex items-center justify-end gap-2 border-t border-edge px-3 py-2">
        {saved && !dirty && <span className="text-[10px] font-semibold uppercase text-emerald-400">Saved</span>}
        <button
          onClick={handleResetDefaults}
          title="Reset the draft below to the game's default settings — still requires Save to apply"
          className="rounded border border-edge px-3 py-1.5 text-xs font-semibold uppercase tracking-wide text-slate-300 hover:border-amber-600 hover:text-amber-300"
        >
          Reset to Defaults
        </button>
        <button
          onClick={handleSave}
          disabled={!dirty}
          className="rounded border border-accent/60 bg-accent/10 px-3 py-1.5 text-xs font-bold uppercase tracking-wide text-accent hover:bg-accent/20 disabled:opacity-40"
        >
          Save Settings
        </button>
      </div>
    </div>
  );
}

// SettingRow — one labelled setting: a title + unit sub-label on the left,
// a plain number input + suffix on the right. The point of this whole
// rework: a number textbox that says exactly what it does, not a vertical
// fader an operator has to interpret.
//
// The input holds its own raw text while focused and only commits (parse,
// clamp, snap to the nearest step) on blur/Enter — clamping on every
// keystroke made it impossible to type a decimal point or a value whose
// first digit was below a non-zero min (e.g. "85" into a 50-100 field: the
// "8" alone clamps up to 50 before "5" can be typed) (s9ui2-02/03).
function SettingRow({
  label, sub, value, min, max, step, suffix, onChange,
}: {
  label: string; sub?: string; value: number; min: number; max: number; step: number;
  suffix?: string; onChange: (v: number) => void;
}) {
  const [text, setText] = useState(String(value));
  const focused = useRef(false);

  // Mirror the committed value into the field, but only while it's not
  // being edited — otherwise a parent re-render would stomp mid-typing.
  useEffect(() => {
    if (!focused.current) setText(String(value));
  }, [value]);

  const commit = () => {
    const n = Number(text);
    if (Number.isNaN(n)) {
      setText(String(value)); // invalid/empty: revert, don't propagate
      return;
    }
    const clamped = Math.min(max, Math.max(min, n));
    // Snap to the nearest step (also rounds lockoutChars to a whole number,
    // since its step is 1) and clean up any float noise from the division.
    const stepped = Math.round(Math.round((clamped - min) / step) * step * 1000) / 1000 + min;
    const final = Math.min(max, Math.max(min, stepped));
    onChange(final);
    setText(String(final));
  };

  return (
    <div className="flex items-center justify-between gap-3 border-b border-edge/50 py-2.5 text-xs">
      <div className="min-w-0">
        <div className="font-semibold text-slate-200">{label}</div>
        {sub && <div className="text-[10px] text-slate-500">{sub}</div>}
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        <input
          type="number"
          inputMode="decimal"
          min={min}
          max={max}
          step={step}
          value={text}
          onFocus={() => { focused.current = true; }}
          onChange={(e) => setText(e.target.value)}
          onBlur={() => { focused.current = false; commit(); }}
          onKeyDown={(e) => {
            if (e.key === "Enter") { commit(); (e.target as HTMLInputElement).blur(); }
            if (e.key === "Escape") { setText(String(value)); (e.target as HTMLInputElement).blur(); }
          }}
          className="w-20 rounded border border-edge bg-panel px-2 py-1 text-right font-mono text-xs text-slate-100 outline-none focus:border-accent"
        />
        {suffix && <span className="w-9 text-[10px] text-slate-500">{suffix}</span>}
      </div>
    </div>
  );
}

// Divider — a labelled gap between groups of related settings.
function Divider({ label }: { label: string }) {
  return (
    <div className="mt-2 flex items-center gap-2 pb-1">
      <span className="text-[9px] font-bold uppercase tracking-widest text-slate-600">{label}</span>
      <div className="h-px flex-1 bg-edge" />
    </div>
  );
}

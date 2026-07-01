import { useState } from "react";
import type {
  AdminViewData,
  GameState,
  ScoreEntry,
} from "@shared/protocol";
import type { AdminActions } from "../useAdmin";
import { PanelHead } from "./BoardPanel";

interface Props {
  gameState?: GameState;
  adminView?: AdminViewData;
  players: ScoreEntry[];
  actions: AdminActions;
}

type Phase = "waiting" | "round" | "buzzed";

const ROUND_STATES: GameState[] = [
  "ROUND_ACTIVE",
  "LOCKED_OUT",
  "ADJUDICATE",
  "KARAOKE",
  "DAILY_DOUBLE",
];

function derivePhase(gameState?: GameState, view?: AdminViewData): Phase {
  if (gameState === "ADJUDICATE" || view?.buzzedHandle) return "buzzed";
  if (
    gameState === "ROUND_ACTIVE" ||
    gameState === "LOCKED_OUT" ||
    gameState === "DAILY_DOUBLE" ||
    gameState === "KARAOKE"
  ) {
    return "round";
  }
  return "waiting";
}

function isRoundActive(s?: GameState): boolean {
  return !!s && ROUND_STATES.includes(s);
}

export default function EvaluationPanel({ gameState, adminView, players, actions }: Props) {
  const phase = derivePhase(gameState, adminView);

  return (
    <section className="flex h-full flex-col bg-panel">
      <PanelHead title="Evaluation" />
      <div className="flex flex-1 flex-col gap-3 overflow-auto p-4">
        <PhaseBanner phase={phase} />
        <BuzzCard view={adminView} />
        <GradeButtons actions={actions} active={phase === "buzzed"} />
        <Overrides actions={actions} players={players} roundActive={isRoundActive(gameState)} />
      </div>
    </section>
  );
}

function PhaseBanner({ phase }: { phase: Phase }) {
  const map: Record<Phase, { label: string; cls: string }> = {
    waiting: { label: "Waiting", cls: "border-slate-700 bg-panel2 text-slate-400" },
    round: { label: "Round Active", cls: "border-sky-800 bg-sky-950/40 text-sky-300" },
    buzzed: { label: "Buzz In — Grade Now", cls: "border-amber-600 bg-amber-950/40 text-amber-300 animate-pulse" },
  };
  const s = map[phase];
  return (
    <div className={`rounded border px-3 py-1.5 text-center text-xs font-bold uppercase tracking-[0.2em] ${s.cls}`}>
      {s.label}
    </div>
  );
}

function BuzzCard({ view }: { view?: AdminViewData }) {
  const handle = view?.buzzedHandle;
  return (
    <div className="rounded-lg border border-edge bg-panel2 p-4">
      <div className="mb-3 flex items-center justify-between">
        <div className="text-[10px] uppercase tracking-wide text-slate-500">Buzzed In</div>
        <div className="font-mono text-xs text-slate-500">
          worth <span className="font-bold text-accent">{view?.currentPoints ?? 0}</span> pts
        </div>
      </div>

      <div
        className={`mb-4 truncate text-3xl font-black ${handle ? "text-white" : "text-slate-700"}`}
        title={handle}
      >
        {handle ?? "— no buzz —"}
      </div>

      <div className="grid grid-cols-2 gap-3">
        <AnswerCell label="Artist" value={view?.correctArtist} />
        <AnswerCell label="Song" value={view?.correctSong} />
      </div>
    </div>
  );
}

function AnswerCell({ label, value }: { label: string; value?: string }) {
  return (
    <div className="rounded border border-edge bg-panel px-3 py-2">
      <div className="text-[10px] uppercase tracking-wide text-slate-500">{label}</div>
      <div className="truncate text-lg font-semibold text-slate-200" title={value}>
        {value ?? "—"}
      </div>
    </div>
  );
}

function GradeButtons({ actions, active }: { actions: AdminActions; active: boolean }) {
  const [partialOpen, setPartialOpen] = useState(false);

  if (!active) return null;

  return (
    <div className="grid grid-cols-3 gap-3">
      <button
        onClick={() => actions.grade({ verdict: "correct" })}
        className="flex h-28 flex-col items-center justify-center rounded-lg border-2 border-emerald-500 bg-emerald-600/80 text-lg font-black uppercase tracking-wide text-white transition hover:bg-emerald-500 active:scale-[0.98]"
      >
        Correct
        <span className="mt-1 text-[10px] font-medium opacity-80">full points</span>
      </button>

      <div className="relative">
        <button
          onClick={() => setPartialOpen((o) => !o)}
          className="flex h-28 w-full flex-col items-center justify-center rounded-lg border-2 border-amber-500 bg-amber-600/80 text-lg font-black uppercase tracking-wide text-white transition hover:bg-amber-500 active:scale-[0.98]"
        >
          Partial
          <span className="mt-1 text-[10px] font-medium opacity-80">artist / song ▾</span>
        </button>
        {partialOpen && (
          <div className="absolute inset-x-0 top-full z-10 mt-1 flex gap-1 rounded-lg border border-edge bg-panel2 p-1 shadow-xl">
            <button
              onClick={() => {
                actions.grade({ verdict: "partial", partialKind: "artist" });
                setPartialOpen(false);
              }}
              className="flex-1 rounded bg-amber-700/60 py-2 text-xs font-bold uppercase text-white hover:bg-amber-600"
            >
              Artist
            </button>
            <button
              onClick={() => {
                actions.grade({ verdict: "partial", partialKind: "song" });
                setPartialOpen(false);
              }}
              className="flex-1 rounded bg-amber-700/60 py-2 text-xs font-bold uppercase text-white hover:bg-amber-600"
            >
              Song
            </button>
          </div>
        )}
      </div>

      <button
        onClick={() => actions.grade({ verdict: "incorrect" })}
        className="flex h-28 flex-col items-center justify-center rounded-lg border-2 border-red-500 bg-red-600/80 text-lg font-black uppercase tracking-wide text-white transition hover:bg-red-500 active:scale-[0.98]"
      >
        Incorrect
        <span className="mt-1 text-[10px] font-medium opacity-80">lock out + resume</span>
      </button>
    </div>
  );
}

function Overrides({ actions, players, roundActive }: { actions: AdminActions; players: ScoreEntry[]; roundActive: boolean }) {
  const [playerID, setPlayerID] = useState("");
  const [delta, setDelta] = useState(0);

  return (
    <div className="rounded-lg border border-edge bg-panel2 p-3">
      <div className="mb-2 text-[10px] uppercase tracking-wide text-slate-500">Manual Overrides</div>

      <div className="mb-3 grid grid-cols-2 gap-2">
        <button
          disabled={!roundActive}
          onClick={() => actions.endRound()}
          className="rounded border border-edge bg-panel py-2 text-xs font-semibold uppercase text-slate-200 hover:border-amber-500 hover:text-amber-300 disabled:pointer-events-none disabled:opacity-30"
        >
          Force End Round
        </button>
        <button
          disabled={!roundActive}
          onClick={() => actions.reveal()}
          className="rounded border border-edge bg-panel py-2 text-xs font-semibold uppercase text-slate-200 hover:border-accent hover:text-accent disabled:pointer-events-none disabled:opacity-30"
        >
          Reveal
        </button>
      </div>

      {/* Award Points: player picker + delta. */}
      <div className="rounded border border-edge bg-panel p-2">
        <div className="mb-2 text-[10px] uppercase tracking-wide text-slate-500">Award Points</div>
        <div className="flex items-center gap-2">
          <select
            value={playerID}
            onChange={(e) => setPlayerID(e.target.value)}
            className="min-w-0 flex-1 rounded border border-edge bg-panel2 px-2 py-1.5 text-xs text-white outline-none focus:border-accent"
          >
            <option value="">Select player…</option>
            {players.map((p) => (
              <option key={p.id} value={p.id}>
                {p.handle} ({p.score})
              </option>
            ))}
          </select>
          <input
            type="number"
            value={delta}
            onChange={(e) => setDelta(Number(e.target.value))}
            className="w-20 rounded border border-edge bg-panel2 px-2 py-1.5 text-right font-mono text-xs text-white outline-none focus:border-accent"
            placeholder="±pts"
          />
          <button
            disabled={!playerID || !delta}
            onClick={() => {
              actions.award({ playerID, delta });
              setDelta(0);
            }}
            className="rounded bg-accent px-3 py-1.5 text-xs font-bold uppercase text-black hover:brightness-110 disabled:opacity-40"
          >
            Award
          </button>
        </div>
      </div>
    </div>
  );
}

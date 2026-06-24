import type { VoteStateData } from "@shared/protocol";

interface Props {
  vote: VoteStateData | null;
  onVote: () => void;
}

export function VoteScreen({ vote, onVote }: Props) {
  const have = vote?.have ?? 0;
  const need = vote?.need ?? 0;
  const voted = vote?.voted ?? false;
  const pct = need > 0 ? Math.min(100, Math.round((have / need) * 100)) : 0;

  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-8 px-6 animate-fadeIn">
      <div className="text-center">
        <div className="text-sm uppercase tracking-[0.3em] text-neutral-500">
          karaoke
        </div>
        <div className="mt-2 text-2xl font-semibold text-neutral-200">
          Sing along…
        </div>
      </div>

      <button
        onPointerDown={(e) => {
          e.preventDefault();
          if (!voted) onVote();
        }}
        disabled={voted}
        className="w-full max-w-sm rounded-2xl bg-guess px-6 py-8 text-2xl font-black text-black transition active:scale-[0.98] disabled:bg-neutral-700 disabled:text-neutral-400"
      >
        {voted ? "Voted ✓" : "Vote for Next Track"}
      </button>

      <div className="w-full max-w-sm">
        <div className="h-3 overflow-hidden rounded-full bg-panel">
          <div
            className="h-full rounded-full bg-guess transition-all duration-300"
            style={{ width: `${pct}%` }}
          />
        </div>
        <div className="mt-2 text-center text-sm text-neutral-400">
          {have} / {need} votes to skip
        </div>
      </div>
    </div>
  );
}

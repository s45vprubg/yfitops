import { useState } from "react";
import type { DailyDoubleSongChoice } from "@shared/protocol";

interface Props {
  // Null until the contestant accepts — then the server pushes the 5 choices.
  // CONTESTANT'S OWN DEVICE ONLY; held in memory only, never persisted (see
  // the ClientMsgType "dailyDouble.offer" doc comment in protocol.ts).
  offer: DailyDoubleSongChoice[] | null;
  onDecide: (accept: boolean) => void;
  onChoose: (trackID: string) => void;
}

export function DailyDoubleContestantScreen({ offer, onDecide, onChoose }: Props) {
  const [decided, setDecided] = useState(false);
  const [chosen, setChosen] = useState<string | null>(null);

  const decide = (accept: boolean) => {
    if (decided) return;
    setDecided(true);
    onDecide(accept);
  };

  const choose = (trackID: string) => {
    if (chosen != null) return;
    setChosen(trackID);
    onChoose(trackID);
  };

  if (!offer) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-8 px-6 animate-fadeIn">
        <div className="text-center">
          <div className="text-sm uppercase tracking-[0.3em] text-yellow-400">daily double</div>
          <div className="mt-2 text-2xl font-semibold text-neutral-200">It's you!</div>
          <div className="mt-1 text-sm text-neutral-500">
            Take the stage and pick a song, or pass on this one.
          </div>
        </div>
        <div className="flex w-full max-w-sm gap-3">
          <button
            onPointerDown={(e) => {
              e.preventDefault();
              decide(false);
            }}
            disabled={decided}
            className="flex-1 rounded-2xl border border-neutral-700 bg-panel px-4 py-6 text-lg font-bold text-neutral-300 transition active:scale-[0.98] disabled:opacity-40"
          >
            Pass
          </button>
          <button
            onPointerDown={(e) => {
              e.preventDefault();
              decide(true);
            }}
            disabled={decided}
            className="flex-1 rounded-2xl bg-guess px-4 py-6 text-lg font-black text-black transition active:scale-[0.98] disabled:opacity-40"
          >
            I'm in!
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-6 px-6 animate-fadeIn">
      <div className="text-center">
        <div className="text-sm uppercase tracking-[0.3em] text-yellow-400">daily double</div>
        <div className="mt-2 text-2xl font-semibold text-neutral-200">
          {chosen == null ? "Pick your song" : "Get ready…"}
        </div>
      </div>
      <div className="flex w-full max-w-sm flex-col gap-2">
        {offer.map((song) => {
          const active = chosen === song.id;
          return (
            <button
              key={song.id}
              onPointerDown={(e) => {
                e.preventDefault();
                choose(song.id);
              }}
              disabled={chosen != null}
              className={`rounded-xl border px-4 py-3 text-left transition active:scale-[0.98] disabled:opacity-40 ${
                active ? "border-guess bg-guess/10" : "border-neutral-700 bg-panel"
              }`}
            >
              <div className="font-semibold text-neutral-100">{song.title}</div>
              <div className="text-sm text-neutral-500">{song.artist}</div>
            </button>
          );
        })}
      </div>
    </div>
  );
}

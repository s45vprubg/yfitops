import { useState } from "react";

interface Props {
  onRate: (stars: number) => void;
}

export function DailyDoubleScreen({ onRate }: Props) {
  const [picked, setPicked] = useState<number | null>(null);
  const [hover, setHover] = useState(0);

  const choose = (stars: number) => {
    if (picked != null) return;
    setPicked(stars);
    onRate(stars);
  };

  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-8 px-6 animate-fadeIn">
      <div className="text-center">
        <div className="text-sm uppercase tracking-[0.3em] text-yellow-400">
          daily double
        </div>
        <div className="mt-2 text-2xl font-semibold text-neutral-200">
          {picked == null ? "Rate your confidence" : "Locked in!"}
        </div>
      </div>

      <div className="flex gap-2">
        {[1, 2, 3, 4, 5].map((n) => {
          const active = (picked ?? hover) >= n;
          return (
            <button
              key={n}
              disabled={picked != null}
              onPointerEnter={() => setHover(n)}
              onPointerLeave={() => setHover(0)}
              onPointerDown={(e) => {
                e.preventDefault();
                choose(n);
              }}
              aria-label={`${n} star${n > 1 ? "s" : ""}`}
              className={`text-5xl transition active:scale-90 ${
                active ? "text-yellow-400" : "text-neutral-700"
              }`}
            >
              ★
            </button>
          );
        })}
      </div>

      {picked != null && (
        <div className="text-sm text-neutral-400">
          {picked} star{picked > 1 ? "s" : ""} wagered
        </div>
      )}
    </div>
  );
}

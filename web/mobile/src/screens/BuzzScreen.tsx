import { useCallback } from "react";

interface Props {
  locked: boolean;
  lockedBy: string | null;
  selfLost: boolean;
  judged: boolean;
  verdict: "partial" | "incorrect" | null;
  onBuzz: () => void;
}

export function BuzzScreen({ locked, lockedBy, judged, verdict, onBuzz }: Props) {
  const handlePointerDown = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault();
      if (locked) return;
      onBuzz();
    },
    [locked, onBuzz],
  );

  if (locked) {
    let overlay: string;
    let icon: string;
    let themeClass: string;
    if (judged && verdict === "partial") {
      overlay = "Nice work! Sit tight while others go for the rest.";
      icon = "🎉";
      themeClass = "text-emerald-400/80";
    } else if (judged && verdict === "incorrect") {
      overlay = "Not quite — you're locked out for this round. Hang tight for the next one!";
      icon = "❌";
      themeClass = "text-amber-400/80";
    } else if (judged) {
      overlay = "Good job — sit tight for the next one";
      icon = "⏳";
      themeClass = "text-amber-400/80";
    } else {
      overlay = lockedBy ? `${lockedBy} is guessing…` : "Locked out";
      icon = "🔒";
      themeClass = "text-danger/80";
    }

    return (
      <div className="flex flex-1 select-none flex-col items-center justify-center bg-locked animate-fadeIn">
        <div className={`text-6xl font-black tracking-tight ${themeClass}`}>
          {icon}
        </div>
        <div className="mt-6 px-8 text-center text-xl font-semibold text-neutral-200">
          {overlay}
        </div>
      </div>
    );
  }

  return (
    <button
      onPointerDown={handlePointerDown}
      className="flex flex-1 select-none touch-none flex-col items-center justify-center bg-guess animate-pulseGlow transition active:scale-[0.97] active:brightness-90"
      aria-label="Guess buzzer"
    >
      <span className="text-7xl font-black tracking-tight text-black sm:text-8xl">
        GUESS
      </span>
      <span className="mt-4 text-sm font-medium uppercase tracking-[0.3em] text-black/60">
        tap to buzz
      </span>
    </button>
  );
}

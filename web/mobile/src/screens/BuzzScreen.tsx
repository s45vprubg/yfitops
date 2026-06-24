import { useCallback } from "react";

interface Props {
  // locked: true => button disabled (someone else guessing / we lost / wrong).
  locked: boolean;
  lockedBy: string | null;
  // distinguishes "I guessed wrong" from "someone else is guessing".
  selfLost: boolean;
  onBuzz: () => void;
}

export function BuzzScreen({ locked, lockedBy, selfLost, onBuzz }: Props) {
  // pointerdown for lowest-latency input; preventDefault stops the synthetic
  // 300ms click / double-tap-zoom on mobile.
  const handlePointerDown = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault();
      if (locked) return;
      onBuzz();
    },
    [locked, onBuzz],
  );

  if (locked) {
    const overlay = selfLost
      ? lockedBy
        ? `Locked: ${lockedBy} is guessing!`
        : "Incorrect — you're out this round"
      : lockedBy
        ? `Locked: ${lockedBy} is guessing!`
        : "Locked out";

    return (
      <div className="flex flex-1 select-none flex-col items-center justify-center bg-locked animate-fadeIn">
        <div className="text-6xl font-black tracking-tight text-danger/80">
          ✕
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

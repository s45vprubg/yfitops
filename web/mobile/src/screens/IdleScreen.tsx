import type { GameState } from "@shared/protocol";

const COPY: Partial<Record<GameState, { title: string; sub: string }>> = {
  LOBBY: { title: "You're in.", sub: "Waiting for the game to start…" },
  BOARD: { title: "Pick incoming…", sub: "Look at the main screen." },
  TRANSITION: { title: "Next track…", sub: "Get ready to buzz." },
  ADJUDICATE: { title: "Hold up…", sub: "A guess is being judged." },
  GAME_OVER: { title: "Game over.", sub: "Check the main screen for scores." },
};

export function IdleScreen({ state }: { state: GameState }) {
  const copy = COPY[state] ?? {
    title: "Waiting for the next track…",
    sub: "Look at the main screen.",
  };
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center animate-fadeIn">
      <div className="text-2xl font-semibold text-neutral-200">
        {copy.title}
      </div>
      <div className="text-sm text-neutral-500">{copy.sub}</div>
      <div className="mt-6 flex gap-1">
        {[0, 1, 2].map((i) => (
          <span
            key={i}
            className="h-2 w-2 animate-pulse rounded-full bg-neutral-600"
            style={{ animationDelay: `${i * 0.2}s` }}
          />
        ))}
      </div>
    </div>
  );
}

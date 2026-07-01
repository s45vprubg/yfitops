import type { GameState, ScoreboardData } from "@shared/protocol";
import { Scoreboard } from "../components/Scoreboard";

const COPY: Partial<Record<GameState, { title: string; sub: string }>> = {
  LOBBY: { title: "You're in.", sub: "Waiting for the game to start…" },
  BOARD: { title: "Pick incoming…", sub: "Look at the main screen." },
  TRANSITION: { title: "Next track…", sub: "Get ready to buzz." },
  ADJUDICATE: { title: "Hold up…", sub: "A guess is being judged." },
  GAME_OVER: { title: "Game over.", sub: "Final scores below." },
};

export function IdleScreen({
  state,
  scoreboard,
  me,
}: {
  state: GameState;
  scoreboard?: ScoreboardData | null;
  me?: string | null;
}) {
  const copy = COPY[state] ?? {
    title: "Waiting for the next track…",
    sub: "Look at the main screen.",
  };
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-4 px-6 text-center animate-fadeIn">
      <div className="text-2xl font-semibold text-neutral-200">
        {copy.title}
      </div>
      <div className="text-sm text-neutral-500">{copy.sub}</div>
      {scoreboard && scoreboard.players.length > 0 ? (
        <Scoreboard scoreboard={scoreboard} me={me} />
      ) : (
        <div className="mt-6 flex gap-1">
          {[0, 1, 2].map((i) => (
            <span
              key={i}
              className="h-2 w-2 animate-pulse rounded-full bg-neutral-600"
              style={{ animationDelay: `${i * 0.2}s` }}
            />
          ))}
        </div>
      )}
    </div>
  );
}

// App — routes the stage view by GameState (§8A) and overlays the persistent
// corner QR (late joiners) and a connection dot.

import { useGame } from "./net/useGame";
import CornerJoin from "./components/CornerJoin";
import Lobby from "./views/Lobby";
import Board from "./views/Board";
import ActiveRound from "./views/ActiveRound";
import Karaoke from "./views/Karaoke";

export default function App() {
  const { view, audio, activateAudio } = useGame();

  const needsActivation = view.spotifyConnectState === "ready" && !view.audioActivated;

  return (
    <div className="crt-overlay relative h-full w-full overflow-hidden">
      <ConnDot connected={view.connected} />

      {needsActivation && <AudioActivationOverlay onActivate={activateAudio} />}

      {renderView()}

      {/* Persistent corner QR on every view except the lobby (which already has
          a giant one). */}
      {view.state !== "LOBBY" && <CornerJoin />}
    </div>
  );

  function renderView() {
    switch (view.state) {
      case "LOBBY":
        return <Lobby scoreboard={view.scoreboard} />;

      case "BOARD":
      case "TRANSITION":
        return <Board board={view.board} />;

      case "ROUND_ACTIVE":
      case "LOCKED_OUT":
      case "DAILY_DOUBLE":
      case "ADJUDICATE":
        if (view.trackStart && view.timer) {
          return (
            <ActiveRound
              trackStart={view.trackStart}
              timer={view.timer}
              reveal={view.reveal}
              revealedArtist={view.revealedArtist}
              revealedSong={view.revealedSong}
              lockoutHandle={view.lockoutHandle}
            />
          );
        }
        return <Board board={view.board} />;

      case "KARAOKE":
        return (
          <Karaoke
            reveal={view.reveal}
            lyrics={view.lyrics}
            scoreboard={view.scoreboard}
            lockoutHandle={view.lockoutHandle}
            gameState={view.state}
            audio={audio}
          />
        );

      case "GAME_OVER":
        return <GameOver scoreboard={view.scoreboard} />;

      default:
        return <Lobby scoreboard={view.scoreboard} />;
    }
  }
}

function ConnDot({ connected }: { connected: boolean }) {
  return (
    <div className="fixed top-4 right-4 z-40 flex items-center gap-2 text-xs text-neon-cyan/60">
      <span
        className={[
          "inline-block h-2.5 w-2.5 rounded-full",
          connected ? "bg-neon-green shadow-[0_0_8px_2px_rgba(53,255,148,0.8)]" : "bg-neon-magenta",
        ].join(" ")}
      />
      {connected ? "live" : "offline"}
    </div>
  );
}

function AudioActivationOverlay({ onActivate }: { onActivate: () => void }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm">
      <button
        onClick={onActivate}
        className="rounded-xl border-2 border-neon-green bg-panel px-12 py-8 text-center shadow-[0_0_30px_rgba(53,255,148,0.3)] transition hover:shadow-[0_0_60px_rgba(53,255,148,0.6)]"
      >
        <div className="mb-3 text-5xl">🔊</div>
        <div className="text-2xl font-bold tracking-wide text-neon-green">Enable Audio</div>
        <div className="mt-2 text-sm text-slate-400">Click to unlock Spotify playback</div>
      </button>
    </div>
  );
}

function GameOver({ scoreboard }: { scoreboard: import("@shared/protocol").ScoreboardData | null }) {
  const players = [...(scoreboard?.players ?? [])].sort((a, b) => b.score - a.score).slice(0, 10);
  return (
    <div className="flex h-full w-full flex-col items-center justify-center px-8">
      <h1 className="mb-10 text-6xl font-extrabold tracking-[0.3em] text-neon-amber neon-text animate-pulseGlow">
        GAME OVER
      </h1>
      <div className="w-full max-w-3xl">
        {players.map((p, i) => (
          <div
            key={p.id}
            className="mb-3 flex items-center justify-between rounded-lg border border-neon-green/20 bg-panel px-6 py-4"
          >
            <span className="flex items-center gap-4">
              <span className="tnum w-10 text-3xl font-extrabold text-neon-cyan/50">{i + 1}</span>
              <span className="text-3xl font-bold text-neon-green">{p.handle}</span>
            </span>
            <span className="tnum text-3xl font-extrabold text-neon-amber neon-text">{p.score}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

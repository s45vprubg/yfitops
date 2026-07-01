// App — routes the stage view by GameState (§8A) and overlays the persistent
// corner QR (late joiners) and a connection dot.

import { useGame } from "./net/useGame";
import CornerJoin from "./components/CornerJoin";
import ScoreOverlay from "./components/ScoreOverlay";
import Lobby from "./views/Lobby";
import Board from "./views/Board";
import ActiveRound from "./views/ActiveRound";
import Karaoke from "./views/Karaoke";

// States where the persistent corner scoreboard is shown. Excludes LOBBY
// (its own big view), KARAOKE and GAME_OVER (which show scores prominently).
const SCORE_OVERLAY_STATES = ["BOARD", "TRANSITION", "ROUND_ACTIVE", "LOCKED_OUT", "ADJUDICATE", "DAILY_DOUBLE"];

export default function App() {
  const { view, audio, activateAudio } = useGame();

  // Show the one-time "Start Stage" gesture whenever Spotify is the audio source
  // and the browser autoplay lock hasn't been cleared yet. It appears from the
  // moment Spotify begins initializing (as a "preparing…" screen) so it's an
  // expected setup step, not a surprise mid-game popup. Browsers require a real
  // user gesture before a tab may emit audio — this click is that gesture.
  const usingSpotify = view.audioMode === "spotify";
  const needsActivation = usingSpotify && !view.audioActivated;

  return (
    <div className="crt-overlay relative h-full w-full overflow-hidden">
      <ConnDot connected={view.connected} />

      {needsActivation && (
        <AudioActivationOverlay
          ready={view.spotifyConnectState === "ready"}
          error={view.spotifyConnectState === "error"}
          onActivate={activateAudio}
        />
      )}

      {renderView()}

      {/* Persistent corner QR on every view except the lobby (which already has
          a giant one). */}
      {view.state !== "LOBBY" && <CornerJoin />}

      {/* Persistent standings during the board/round flow. */}
      {SCORE_OVERLAY_STATES.includes(view.state) && <ScoreOverlay scoreboard={view.scoreboard} />}
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
              maskedReveal={view.maskedReveal}
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
            lyricsStatus={view.lyricsStatus}
            lockoutHandle={view.lockoutHandle}
            roundWinner={view.roundWinner}
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

// AudioActivationOverlay is the stage's one-time "Start Stage" gate. It is shown
// from the moment Spotify begins initializing so the operator expects it:
//   - preparing  → SDK still connecting; button disabled
//   - ready      → click to satisfy the browser autoplay gesture + start audio
//   - error      → SDK failed (bad token / not Premium); offer a retry click
// After a successful click the browser unlocks audio for the session and this
// never reappears.
function AudioActivationOverlay({
  ready,
  error,
  onActivate,
}: {
  ready: boolean;
  error: boolean;
  onActivate: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/85 backdrop-blur-sm">
      <button
        onClick={ready || error ? onActivate : undefined}
        disabled={!ready && !error}
        className={[
          "rounded-xl border-2 bg-panel px-16 py-10 text-center transition",
          ready || error
            ? "cursor-pointer border-neon-green shadow-[0_0_30px_rgba(53,255,148,0.3)] hover:shadow-[0_0_60px_rgba(53,255,148,0.6)]"
            : "cursor-default border-neon-cyan/40 opacity-80",
        ].join(" ")}
      >
        <div className="mb-4 text-6xl">{error ? "⚠️" : "🎵"}</div>
        {!ready && !error && (
          <>
            <div className="text-3xl font-bold tracking-wide text-neon-cyan">Preparing stage…</div>
            <div className="mt-2 text-sm text-slate-400">Connecting to Spotify</div>
          </>
        )}
        {ready && (
          <>
            <div className="text-3xl font-bold tracking-wide text-neon-green neon-text">Start Stage</div>
            <div className="mt-2 text-sm text-slate-400">Click once to enable audio and begin</div>
          </>
        )}
        {error && (
          <>
            <div className="text-3xl font-bold tracking-wide text-neon-magenta">Audio unavailable</div>
            <div className="mt-2 max-w-md text-sm text-slate-400">
              Spotify didn't connect. Check the account is Premium and reconnected in the control room, then click to retry.
            </div>
          </>
        )}
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

import { useGame } from "./lib/useGame";
import { StatusBar } from "./components/StatusBar";
import { RevealStrip } from "./components/RevealStrip";
import { JoinScreen } from "./screens/JoinScreen";
import { IdleScreen } from "./screens/IdleScreen";
import { BuzzScreen } from "./screens/BuzzScreen";
import { VoteScreen } from "./screens/VoteScreen";
import { DailyDoubleScreen } from "./screens/DailyDoubleScreen";

// Pure state-flag router (§4A): every branch below keys off the server's
// GameState + sanitized payloads. No track metadata ever enters this tree.
export default function App() {
  const { view, connect, buzz, vote, rate } = useGame();

  if (!view.joined) {
    return (
      <JoinScreen conn={view.conn} error={view.error} onJoin={connect} />
    );
  }

  // The streaming decrypt reveal band, shown while a round is live and at
  // karaoke. Renders the exact same server mask as the projector (§4A ext).
  const revealBand = (
    <div className="flex shrink-0 items-center justify-center border-t border-neutral-800 bg-bg py-4">
      <RevealStrip mask={view.maskedReveal} />
    </div>
  );

  const renderScreen = () => {
    switch (view.state) {
      case "ROUND_ACTIVE":
        return (
          <div className="flex flex-1 flex-col">
            <BuzzScreen
              locked={view.buzzedAndLost}
              lockedBy={view.lockedBy}
              selfLost={view.buzzedAndLost}
              judged={view.judgedThisRound}
              onBuzz={buzz}
            />
            {revealBand}
          </div>
        );

      case "LOCKED_OUT":
        return (
          <div className="flex flex-1 flex-col">
            <BuzzScreen
              locked
              lockedBy={view.lockedBy}
              selfLost={view.buzzedAndLost}
              judged={view.judgedThisRound}
              onBuzz={buzz}
            />
            {revealBand}
          </div>
        );

      case "KARAOKE":
        return (
          <div className="flex flex-1 flex-col">
            <VoteScreen vote={view.vote} onVote={vote} />
            {revealBand}
          </div>
        );

      case "DAILY_DOUBLE":
        return <DailyDoubleScreen onRate={rate} />;

      // LOBBY, BOARD, TRANSITION, ADJUDICATE, GAME_OVER
      default:
        return <IdleScreen state={view.state} />;
    }
  };

  return (
    <>
      <StatusBar conn={view.conn} rttMs={view.rttMs} />
      {renderScreen()}
    </>
  );
}

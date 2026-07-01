import { useGame } from "./lib/useGame";
import { StatusBar } from "./components/StatusBar";
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

  const renderScreen = () => {
    switch (view.state) {
      case "ROUND_ACTIVE":
        return (
          <BuzzScreen
            locked={view.buzzedAndLost}
            lockedBy={view.lockedBy}
            selfLost={view.buzzedAndLost}
            judged={view.judgedThisRound}
            verdict={view.lastVerdict}
            onBuzz={buzz}
          />
        );

      case "LOCKED_OUT":
        return (
          <BuzzScreen
            locked
            lockedBy={view.lockedBy}
            selfLost={view.buzzedAndLost}
            judged={view.judgedThisRound}
            verdict={view.lastVerdict}
            onBuzz={buzz}
          />
        );

      case "KARAOKE":
        return <VoteScreen vote={view.vote} onVote={vote} />;

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

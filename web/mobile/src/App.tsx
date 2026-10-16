import { useGame } from "./lib/useGame";
import { StatusBar } from "./components/StatusBar";
import { RevealStrip } from "./components/RevealStrip";
import { JoinScreen } from "./screens/JoinScreen";
import { IdleScreen } from "./screens/IdleScreen";
import { BuzzScreen } from "./screens/BuzzScreen";
import { VoteScreen } from "./screens/VoteScreen";
import { DailyDoubleScreen } from "./screens/DailyDoubleScreen";
import { DailyDoubleContestantScreen } from "./screens/DailyDoubleContestantScreen";
import { DailyDoubleWaitingScreen } from "./screens/DailyDoubleWaitingScreen";
import { PickCellScreen } from "./screens/PickCellScreen";
import { getSavedHandle } from "./lib/fingerprint";

// Pure state-flag router (§4A): every branch below keys off the server's
// GameState + sanitized payloads. No track metadata ever enters this tree —
// EXCEPT view.ddOffer, which is the deliberate, narrowly-scoped §4A exception
// for the Daily Double contestant's own device only (see protocol.ts).
export default function App() {
  const { view, connect, buzz, vote, rate, ddDecide, ddChoose, selectCell } = useGame();
  const iAmPicker = !!view.pickerStatus && view.pickerStatus.pickerID === view.myPlayerID;
  const stillPicking = iAmPicker && !view.pickerStatus!.hasPicked;

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

  // s8uim-02: neutral stand-in for the brief pickerSettling window, same
  // visual language as PickCellScreen's own "Loading the board…" state so
  // it doesn't look like a new/different kind of screen.
  const settlingPlaceholder = (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center animate-fadeIn">
      <div className="text-sm uppercase tracking-[0.3em] text-yellow-400">one sec</div>
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
              verdict={view.lastVerdict}
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
              verdict={view.lastVerdict}
              onBuzz={buzz}
            />
            {revealBand}
          </div>
        );

      case "KARAOKE":
        // The designated picker (whoever most recently scored) picks the
        // next cell here first; everyone else — and the picker too, once
        // they've picked — votes on skipping ahead like today. Both
        // progress independently (the server's AND-gate fires the
        // transition once BOTH have happened, in either order), so
        // non-pickers are never blocked from voting while a pick is
        // pending.
        //
        // s8uim-02: `state` -> KARAOKE arrives before the trailing
        // pickerStatus broadcast. During that brief gap pickerSettling is
        // true, so render a neutral placeholder instead of defaulting to
        // VoteScreen — which would otherwise flash on the eventual picker's
        // own device for an instant before flipping to PickCellScreen.
        return (
          <div className="flex flex-1 flex-col">
            {view.pickerSettling ? (
              settlingPlaceholder
            ) : stillPicking ? (
              <PickCellScreen board={view.pickableBoard} onSelect={selectCell} />
            ) : (
              <VoteScreen vote={view.vote} onVote={vote} />
            )}
            {revealBand}
          </div>
        );

      case "BOARD":
        // Normally admin-only (falls to the idle screen below). The one
        // exception: right after a Daily Double, which goes straight to
        // BOARD with no karaoke phase, the performer becomes the picker —
        // same rule as a normal correct guess — with no vote to wait on.
        //
        // s8uim-02: same trailing-message gap as KARAOKE above (finishDaily
        // Double transitions to BOARD before broadcasting pickerStatus).
        // This also fires on an ordinary admin-picks BOARD entry where no
        // pickerStatus is ever coming — pickerSettling's own fallback timer
        // (useGame.ts, PICKER_SETTLE_MS) bounds that to a brief, one-time
        // placeholder rather than a lasting change to the idle screen.
        if (view.pickerSettling) {
          return settlingPlaceholder;
        }
        if (view.pickerStatus && !view.pickerStatus.hasPicked) {
          return stillPicking ? (
            <PickCellScreen board={view.pickableBoard} onSelect={selectCell} />
          ) : (
            <DailyDoubleWaitingScreen
              title={`${view.pickerStatus.pickerHandle} is picking the next cell…`}
              sub="Hang tight."
            />
          );
        }
        return (
          <IdleScreen state={view.state} scoreboard={view.scoreboard} me={getSavedHandle()} ddResult={view.ddResult} />
        );

      case "DAILY_DOUBLE": {
        const performer = view.ddPerformer;
        if (!performer) {
          // No status yet (resync in flight) — same idle fallback as below.
          return (
            <IdleScreen
              state={view.state}
              scoreboard={view.scoreboard}
              me={getSavedHandle()}
              ddResult={view.ddResult}
            />
          );
        }
        const iAmContestant = performer.playerID === view.myPlayerID;
        if (iAmContestant) {
          return performer.performing ? (
            <DailyDoubleWaitingScreen title="You're up!" sub="Sing along on the big screen." />
          ) : (
            <DailyDoubleContestantScreen offer={view.ddOffer} onDecide={ddDecide} onChoose={ddChoose} />
          );
        }
        return performer.performing ? (
          <DailyDoubleScreen onRate={rate} performerHandle={performer.handle} />
        ) : (
          <DailyDoubleWaitingScreen
            title={`${performer.handle} hit a Daily Double!`}
            sub="Waiting for them to decide…"
          />
        );
      }

      // LOBBY, TRANSITION, ADJUDICATE, GAME_OVER (BOARD is handled above)
      default:
        return (
          <IdleScreen
            state={view.state}
            scoreboard={view.scoreboard}
            me={getSavedHandle()}
            ddResult={view.ddResult}
          />
        );
    }
  };

  return (
    <>
      <StatusBar conn={view.conn} rttMs={view.rttMs} />
      {renderScreen()}
    </>
  );
}

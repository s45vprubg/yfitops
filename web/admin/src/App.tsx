import { useEffect, useState } from "react";
import { useAdmin } from "./useAdmin";
import type { GameState } from "@shared/protocol";
import Login from "./components/Login";
import TopBar from "./components/TopBar";
import BoardPanel from "./components/BoardPanel";
import EvaluationPanel from "./components/EvaluationPanel";
import TelemetryPanel from "./components/TelemetryPanel";
import ScorePanel from "./components/ScorePanel";
import BoardBuilderPage from "./components/BoardBuilderPage";
import SettingsPage from "./components/SettingsPage";
import { HTTP_URL } from "./config";

const ACTIVE_GAME_STATES: GameState[] = [
  "BOARD", "ROUND_ACTIVE", "LOCKED_OUT", "ADJUDICATE",
  "KARAOKE", "DAILY_DOUBLE", "TRANSITION",
];

type Page = "control" | "builder" | "settings";

export default function App() {
  const [state, actions] = useAdmin();
  const [page, setPage] = useState<Page>("control");
  const [spotifyConnected, setSpotifyConnected] = useState(false);

  const authed = state.status === "authed";
  const secret = state.adminSecret ?? "";

  useEffect(() => {
    if (!authed || !secret) return;
    let stop = false;
    const check = async () => {
      try {
        const res = await fetch(`${HTTP_URL}/api/spotify/token`, {
          headers: { Authorization: `Bearer ${secret}` },
          cache: "no-store",
        });
        if (!stop) setSpotifyConnected(res.ok);
      } catch {
        if (!stop) setSpotifyConnected(false);
      }
    };
    check();
    const id = setInterval(check, 5000);
    return () => { stop = true; clearInterval(id); };
  }, [authed, secret]);

  if (!authed) {
    return (
      <Login status={state.status} error={state.error} onSubmit={actions.login} />
    );
  }

  const players = state.scoreboard?.players ?? [];

  return (
    <div className="flex h-full w-full flex-col bg-[#05070a] text-slate-200">
      {/* Top header: brand (far left) · tabs · Spotify status + lock (far right) */}
      <nav className="flex items-center gap-2 border-b border-edge bg-panel px-4 py-1">
        <span className="mr-3 text-sm font-bold tracking-[0.25em] text-accent">YFITOPS</span>
        {([
          ["control", "Control Room"],
          ["builder", "Board Builder"],
          ["settings", "Settings"],
        ] as [Page, string][]).map(([id, label]) => (
          <button
            key={id}
            onClick={() => setPage(id)}
            className={`rounded px-3 py-1 text-xs font-semibold ${
              page === id ? "bg-accent/20 text-accent" : "text-slate-400 hover:text-slate-200"
            }`}
          >
            {label}
          </button>
        ))}
        <div className="flex-1" />
        <SpotifyStatus connected={spotifyConnected} />
        <button
          onClick={actions.logout}
          title="Lock control room"
          aria-label="Lock control room"
          className="rounded p-1.5 text-slate-400 hover:bg-panel3 hover:text-white"
        >
          {/* padlock icon */}
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <rect x="3" y="11" width="18" height="11" rx="2" ry="2" />
            <path d="M7 11V7a5 5 0 0 1 10 0v4" />
          </svg>
        </button>
      </nav>

      {page === "control" && (
        <>
          <TopBar
            status={state.status}
            connected={state.connected}
            nonce={state.nonce}
            gameState={state.gameState}
            actions={actions}
            adminSecret={secret}
            spotifyConnected={spotifyConnected}
          />

          <main className="grid min-h-0 flex-1 grid-cols-[minmax(260px,1fr)_minmax(420px,1.6fr)_minmax(300px,1fr)]">
            <BoardPanel
              board={state.board}
              gameState={state.gameState}
              spotifyConnected={spotifyConnected}
              adminSecret={secret}
              onSelect={(row, col) => actions.select({ row, col })}
            />
            <EvaluationPanel
              gameState={state.gameState}
              adminView={state.adminView}
              players={players}
              actions={actions}
            />
            {/* Right column split: live telemetry on top, ranked scoreboard below. */}
            <div className="grid min-h-0 grid-rows-[1.4fr_1fr]">
              <TelemetryPanel telemetry={state.telemetry} actions={actions} />
              <ScorePanel players={players} />
            </div>
          </main>

          {/* Spotify disconnected mid-game warning */}
          {!spotifyConnected && !!state.gameState && ACTIVE_GAME_STATES.includes(state.gameState) && (
            <div className="pointer-events-none fixed inset-x-0 top-24 flex justify-center p-3 z-50">
              <div className="pointer-events-auto rounded border border-amber-600 bg-amber-950/90 px-5 py-2.5 text-sm font-semibold text-amber-200 shadow-xl">
                Spotify disconnected — audio commands will fail. Reconnect via the button above.
              </div>
            </div>
          )}

          {/* Transient operational notice (e.g. "busy: round in progress").
              Does NOT log the admin out — just informs and can be dismissed. */}
          {state.notice && (
            <div className="pointer-events-none fixed inset-x-0 bottom-0 flex justify-center p-3">
              <div className="pointer-events-auto rounded border border-amber-700 bg-amber-950/85 px-4 py-2 text-sm text-amber-100 shadow-xl">
                {state.notice}
                <button onClick={actions.clearNotice} className="ml-3 underline">
                  Dismiss
                </button>
              </div>
            </div>
          )}

          {!state.connected && (
            <div className="pointer-events-none fixed inset-x-0 bottom-0 flex justify-center p-3">
              <div className="pointer-events-auto rounded border border-red-800 bg-red-950/80 px-4 py-2 text-sm text-red-200 shadow-xl">
                Connection lost — re-authenticate to resync.{" "}
                <button onClick={actions.logout} className="ml-2 underline">
                  Re-login
                </button>
              </div>
            </div>
          )}
        </>
      )}

      {page === "builder" && (
        <div className="min-h-0 flex-1">
          <BoardBuilderPage secret={secret} />
        </div>
      )}

      {page === "settings" && (
        <div className="min-h-0 flex-1">
          <SettingsPage revealCfg={state.revealCfg} actions={actions} />
        </div>
      )}
    </div>
  );
}

// SpotifyStatus — header indicator. Connected: green Spotify glyph with a live
// dot. Disconnected: a subtle "connect" affordance that opens the OAuth flow.
function SpotifyStatus({ connected }: { connected: boolean }) {
  if (connected) {
    return (
      <span className="flex items-center gap-1.5 px-1" title="Spotify connected">
        <SpotifyGlyph className="text-green-400" />
        <span className="h-2 w-2 rounded-full bg-green-400 shadow-[0_0_6px_1px_rgba(74,222,128,0.7)]" />
      </span>
    );
  }
  return (
    <button
      onClick={() => window.open(`${HTTP_URL}/auth/spotify`, "_blank", "noopener")}
      title="Connect Spotify"
      className="flex items-center gap-1.5 rounded px-2 py-1 text-xs font-semibold text-slate-400 hover:bg-panel3 hover:text-green-300"
    >
      <SpotifyGlyph className="text-slate-500" />
      connect
    </button>
  );
}

function SpotifyGlyph({ className }: { className?: string }) {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden>
      <path d="M12 2a10 10 0 100 20 10 10 0 000-20zm4.586 14.424a.622.622 0 01-.857.207c-2.348-1.435-5.304-1.76-8.785-.964a.622.622 0 11-.277-1.213c3.809-.871 7.077-.496 9.712 1.114a.622.622 0 01.207.856zm1.223-2.722a.778.778 0 01-1.07.257c-2.688-1.652-6.786-2.13-9.965-1.166a.778.778 0 11-.452-1.49c3.632-1.102 8.147-.568 11.23 1.329a.778.778 0 01.257 1.07zm.105-2.835C14.692 8.95 9.375 8.775 6.29 9.712a.933.933 0 11-.542-1.786c3.541-1.075 9.412-.868 13.115 1.33a.933.933 0 01-.95 1.606z" />
    </svg>
  );
}

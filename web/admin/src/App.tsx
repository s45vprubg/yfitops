import { useState } from "react";
import { useAdmin } from "./useAdmin";
import Login from "./components/Login";
import TopBar from "./components/TopBar";
import BoardPanel from "./components/BoardPanel";
import EvaluationPanel from "./components/EvaluationPanel";
import TelemetryPanel from "./components/TelemetryPanel";
import BoardBuilderPage from "./components/BoardBuilderPage";

type Page = "control" | "builder";

export default function App() {
  const [state, actions] = useAdmin();
  const [page, setPage] = useState<Page>("control");

  const authed = state.status === "authed";

  if (!authed) {
    return (
      <Login status={state.status} error={state.error} onSubmit={actions.login} />
    );
  }

  const players = state.scoreboard?.players ?? [];

  return (
    <div className="flex h-full w-full flex-col bg-[#05070a] text-slate-200">
      {/* Navigation tabs */}
      <nav className="flex items-center gap-1 border-b border-edge bg-panel px-4 py-1">
        <button
          onClick={() => setPage("control")}
          className={`rounded px-3 py-1 text-xs font-semibold ${
            page === "control"
              ? "bg-accent/20 text-accent"
              : "text-slate-400 hover:text-slate-200"
          }`}
        >
          Control Room
        </button>
        <button
          onClick={() => setPage("builder")}
          className={`rounded px-3 py-1 text-xs font-semibold ${
            page === "builder"
              ? "bg-accent/20 text-accent"
              : "text-slate-400 hover:text-slate-200"
          }`}
        >
          Board Builder
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
            onLogout={actions.logout}
            adminSecret={state.adminSecret ?? ""}
          />

          <main className="grid min-h-0 flex-1 grid-cols-[minmax(260px,1fr)_minmax(420px,1.6fr)_minmax(300px,1fr)]">
            <BoardPanel
              board={state.board}
              onSelect={(row, col) => actions.select({ row, col })}
            />
            <EvaluationPanel
              gameState={state.gameState}
              adminView={state.adminView}
              players={players}
              actions={actions}
            />
            <TelemetryPanel telemetry={state.telemetry} actions={actions} />
          </main>

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
          <BoardBuilderPage secret={state.adminSecret ?? ""} />
        </div>
      )}
    </div>
  );
}

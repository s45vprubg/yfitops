import { memo, useMemo } from "react";
import type { TelemetryConn, TelemetryData } from "@shared/protocol";
import type { AdminActions, CheatEntry, CheatReport } from "../useAdmin";
import { Empty, PanelHead } from "./BoardPanel";
import { useModal } from "./Modal";

interface Props {
  telemetry?: TelemetryData;
  cheat?: CheatReport;
  actions: AdminActions;
}

// Right column (Telemetry): live connection log with per-user RTT, score,
// active flag, anti-cheat signals (IP + shared-ip/multi-conn flags), and
// Kick / Ban quick actions.
//
// Join-burst fix: this list scales toward 500 rows at a real event, and had
// no memoization anywhere — every incoming broadcast (even an unrelated one,
// before the engine-side coalescing fix) forced a full unmemoized re-render
// of every row. Memoized here as a second layer of defense on top of that
// server-side fix, since this list will still legitimately update roughly
// once a second under load even after coalescing.
function TelemetryPanel({ telemetry, cheat, actions }: Props) {
  const conns = telemetry?.connections ?? [];
  const cheatByID = useMemo(() => {
    const m = new Map<string, CheatEntry>();
    for (const e of cheat?.players ?? []) m.set(e.playerId, e);
    return m;
  }, [cheat]);
  const activeCount = useMemo(() => conns.filter((c) => c.active).length, [conns]);
  const flaggedCount = useMemo(
    () => (cheat?.players ?? []).filter((e) => e.flags.length > 0).length,
    [cheat],
  );

  return (
    <section className="flex h-full flex-col border-l border-edge bg-panel2">
      <PanelHead
        title="Telemetry"
        hint={flaggedCount > 0 ? `⚠ ${flaggedCount} flagged · ${activeCount}/${conns.length} active` : `${activeCount}/${conns.length} active`}
      />
      <div className="flex-1 overflow-auto">
        {conns.length === 0 ? (
          <Empty>No connections</Empty>
        ) : (
          <table className="w-full border-collapse text-xs">
            <thead className="sticky top-0 bg-panel3 text-[10px] uppercase tracking-wide text-slate-400">
              <tr>
                <th className="px-2 py-1.5 text-left font-semibold">Handle</th>
                <th className="px-2 py-1.5 text-right font-semibold">RTT</th>
                <th className="px-2 py-1.5 text-right font-semibold">Score</th>
                <th className="px-2 py-1.5 text-right font-semibold">Actions</th>
              </tr>
            </thead>
            <tbody>
              {conns.map((c) => (
                <Row key={c.id} c={c} cheat={cheatByID.get(c.id)} actions={actions} />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </section>
  );
}

export default memo(TelemetryPanel);

function rttColor(rtt: number): string {
  if (rtt <= 0) return "text-slate-600";
  if (rtt < 60) return "text-emerald-400";
  if (rtt < 150) return "text-amber-400";
  return "text-red-400";
}

type RowProps = { c: TelemetryConn; cheat?: CheatEntry; actions: AdminActions };

function rowPropsEqual(prev: RowProps, next: RowProps): boolean {
  // The server sends a fresh object graph on every broadcast even when a
  // given row's actual values haven't changed, so default referential
  // prop comparison in memo() would never skip anything — compare the
  // real fields instead.
  const pc = prev.c, nc = next.c;
  if (pc.id !== nc.id || pc.handle !== nc.handle || pc.rttMs !== nc.rttMs || pc.score !== nc.score || pc.active !== nc.active) {
    return false;
  }
  const pChe = prev.cheat, nChe = next.cheat;
  if (!pChe !== !nChe) return false;
  if (pChe && nChe) {
    if (pChe.ip !== nChe.ip || pChe.conns !== nChe.conns) return false;
    if (pChe.flags.length !== nChe.flags.length || pChe.flags.some((f, i) => f !== nChe.flags[i])) return false;
  }
  return prev.actions === next.actions;
}

const Row = memo(function Row({ c, cheat, actions }: RowProps) {
  const { confirm } = useModal();
  const flags = cheat?.flags ?? [];
  return (
    <tr className="border-b border-edge/50 hover:bg-panel3/40">
      <td className="px-2 py-1.5">
        <div className="flex items-center gap-1.5">
          <span
            className={`inline-block h-2 w-2 shrink-0 rounded-full ${c.active ? "bg-emerald-400" : "bg-slate-600"}`}
            title={c.active ? "active" : "inactive"}
          />
          <span className="truncate font-medium text-slate-100" title={c.handle}>
            {c.handle || "—"}
          </span>
        </div>
        {/* Anti-cheat line: IP + flag chips. */}
        <div className="mt-0.5 flex items-center gap-1 pl-3.5">
          {cheat?.ip && <span className="font-mono text-[9px] text-slate-500">{cheat.ip}</span>}
          {flags.includes("shared-ip") && (
            <span className="rounded bg-red-950/60 px-1 text-[9px] font-bold uppercase text-red-300" title="Another player shares this IP">
              shared IP
            </span>
          )}
          {flags.includes("multi-conn") && (
            <span className="rounded bg-amber-950/60 px-1 text-[9px] font-bold uppercase text-amber-300" title={`${cheat?.conns} live connections for this player`}>
              {cheat?.conns}× conn
            </span>
          )}
        </div>
      </td>
      <td className={`px-2 py-1.5 text-right font-mono ${rttColor(c.rttMs)}`}>
        {c.rttMs > 0 ? `${Math.round(c.rttMs)}ms` : "—"}
      </td>
      <td className="px-2 py-1.5 text-right font-mono text-slate-200">{c.score}</td>
      <td className="px-2 py-1.5 text-right">
        <div className="flex justify-end gap-1">
          <button
            onClick={() => actions.kick({ playerID: c.id, ban: false })}
            className="rounded border border-edge bg-panel px-2 py-0.5 text-[10px] font-semibold uppercase text-amber-300 hover:border-amber-500"
            title="Disconnect this player"
          >
            Kick
          </button>
          <button
            onClick={async () => {
              if (
                await confirm({
                  title: "Ban player?",
                  body: `Ban ${c.handle || "this player"}? They will not be able to rejoin.`,
                  confirmLabel: "Ban",
                  danger: true,
                })
              ) {
                actions.kick({ playerID: c.id, ban: true });
              }
            }}
            className="rounded border border-red-800 bg-red-950/40 px-2 py-0.5 text-[10px] font-bold uppercase text-red-300 hover:bg-red-900/50"
            title="Ban this player"
          >
            Ban
          </button>
        </div>
      </td>
    </tr>
  );
}, rowPropsEqual);

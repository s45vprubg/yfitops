import type { ConnStatus } from "../lib/useGame";

interface Props {
  conn: ConnStatus;
  rttMs: number | null;
}

const DOT: Record<ConnStatus, string> = {
  idle: "bg-neutral-600",
  connecting: "bg-yellow-400 animate-pulse",
  connected: "bg-guess",
  disconnected: "bg-danger animate-pulse",
};

const LABEL: Record<ConnStatus, string> = {
  idle: "offline",
  connecting: "connecting",
  connected: "live",
  disconnected: "reconnecting",
};

export function StatusBar({ conn, rttMs }: Props) {
  return (
    <div className="flex items-center justify-between px-4 py-2 text-[11px] uppercase tracking-widest text-neutral-500">
      <div className="flex items-center gap-2">
        <span className={`h-2 w-2 rounded-full ${DOT[conn]}`} />
        {LABEL[conn]}
      </div>
      {rttMs != null && conn === "connected" && (
        <span>{rttMs}ms</span>
      )}
    </div>
  );
}

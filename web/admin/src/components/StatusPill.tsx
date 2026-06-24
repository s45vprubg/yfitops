import type { ConnStatus } from "../useAdmin";

interface Props {
  connected: boolean;
  status: ConnStatus;
  nonce: number;
}

// Connection status indicator. Green = authed + live, amber = connecting,
// red = dropped/forbidden.
export default function StatusPill({ connected, status, nonce }: Props) {
  let color = "bg-slate-500";
  let label = "idle";
  if (status === "authed" && connected) {
    color = "bg-emerald-400";
    label = "live";
  } else if (status === "connecting") {
    color = "bg-amber-400";
    label = "connecting";
  } else if (status === "error") {
    color = "bg-red-500";
    label = "error";
  } else if (!connected) {
    color = "bg-red-500";
    label = "disconnected";
  }

  return (
    <div className="flex items-center gap-2 rounded border border-edge bg-panel px-3 py-1.5 font-mono text-xs">
      <span className={`inline-block h-2.5 w-2.5 rounded-full ${color} ${label === "live" ? "animate-pulse" : ""}`} />
      <span className="uppercase tracking-wide text-slate-300">{label}</span>
      <span className="text-slate-500">· n{nonce}</span>
    </div>
  );
}

import { useState, type FormEvent } from "react";
import type { ConnStatus } from "../useAdmin";

interface Props {
  status: ConnStatus;
  error?: string;
  onSubmit: (secret: string) => void;
}

// §9: a login screen takes the ADMIN_SECRET password. On submit we connect and
// send Hello{role:"admin", adminSecret}. A server "forbidden" surfaces here.
export default function Login({ status, error, onSubmit }: Props) {
  const [secret, setSecret] = useState("");
  const busy = status === "connecting";

  function submit(e: FormEvent) {
    e.preventDefault();
    if (secret.trim()) onSubmit(secret);
  }

  return (
    <div className="flex h-full w-full items-center justify-center bg-[#05070a]">
      <form
        onSubmit={submit}
        className="w-[360px] rounded-lg border border-edge bg-panel2 p-8 shadow-2xl"
      >
        <div className="mb-1 text-xs font-semibold uppercase tracking-[0.3em] text-accent">
          yfitops
        </div>
        <h1 className="mb-6 text-2xl font-bold text-white">Control Room</h1>

        <label className="mb-2 block text-xs uppercase tracking-wide text-slate-400">
          Admin Secret
        </label>
        <input
          type="password"
          autoFocus
          autoComplete="current-password"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          disabled={busy}
          placeholder="ADMIN_SECRET"
          className="mb-4 w-full rounded border border-edge bg-panel px-3 py-2 font-mono text-sm text-white outline-none focus:border-accent disabled:opacity-50"
        />

        <button
          type="submit"
          disabled={busy || !secret.trim()}
          className="w-full rounded bg-accent py-2 font-semibold text-black transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {busy ? "Connecting…" : "Authenticate"}
        </button>

        {status === "error" && error && (
          <div className="mt-4 rounded border border-red-700/60 bg-red-950/50 px-3 py-2 text-sm text-red-300">
            {error}
          </div>
        )}

        <p className="mt-6 text-[11px] leading-relaxed text-slate-500">
          The secret must match the backend's <code>ADMIN_SECRET</code>. The
          client holds no game state — all authority lives in the server.
        </p>
      </form>
    </div>
  );
}

import { useState, type FormEvent } from "react";
import type { AuthStatus } from "../net/useGame";

interface Props {
  status: AuthStatus;
  error?: string;
  onSubmit: (secret: string) => void;
}

// Stage login — takes the same ADMIN_SECRET password as web/admin's Login.tsx
// (the server checks stage and admin against the identical secret). Entered
// at runtime so the secret is never baked into the public build; previously
// the stage auto-connected on page load with a build-time-embedded secret,
// which let anyone who loaded the page control playback with zero auth.
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
        className="w-[360px] rounded-lg border border-neon-green/30 bg-panel p-8 shadow-2xl"
      >
        <div className="mb-1 text-xs font-semibold uppercase tracking-[0.3em] text-neon-green">
          yfitops
        </div>
        <h1 className="mb-6 text-2xl font-bold text-white">Stage Login</h1>

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
          className="mb-4 w-full rounded border border-neon-green/20 bg-[#0a0d12] px-3 py-2 font-mono text-sm text-white outline-none focus:border-neon-green disabled:opacity-50"
        />

        <button
          type="submit"
          disabled={busy || !secret.trim()}
          className="w-full rounded bg-neon-green py-2 font-semibold text-black transition hover:brightness-110 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {busy ? "Connecting…" : "Start Stage"}
        </button>

        {status === "error" && error && (
          <div className="mt-4 rounded border border-red-700/60 bg-red-950/50 px-3 py-2 text-sm text-red-300">
            {error}
          </div>
        )}
      </form>
    </div>
  );
}

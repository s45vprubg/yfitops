import { useState, type FormEvent } from "react";
import { getSavedHandle } from "../lib/fingerprint";
import type { ConnStatus } from "../lib/useGame";

interface Props {
  conn: ConnStatus;
  error: string | null;
  onJoin: (handle: string) => void;
}

export function JoinScreen({ conn, error, onJoin }: Props) {
  const [handle, setHandle] = useState(getSavedHandle());
  const connecting = conn === "connecting";

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const h = handle.trim();
    if (h.length === 0 || connecting) return;
    onJoin(h);
  };

  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-8 px-6 animate-fadeIn">
      <div className="text-center">
        <h1 className="text-4xl font-bold tracking-tight text-guess">
          yfitops
        </h1>
        <p className="mt-1 text-sm uppercase tracking-[0.3em] text-neutral-500">
          buzzer
        </p>
      </div>

      <form onSubmit={submit} className="flex w-full max-w-sm flex-col gap-4">
        <label className="flex flex-col gap-2">
          <span className="text-xs uppercase tracking-widest text-neutral-400">
            Hacker Handle
          </span>
          <input
            value={handle}
            onChange={(e) => setHandle(e.target.value)}
            maxLength={24}
            autoCapitalize="off"
            autoComplete="off"
            spellCheck={false}
            placeholder="0xR00K"
            className="rounded-xl border border-neutral-700 bg-panel px-4 py-4 text-lg text-white outline-none focus:border-guess"
          />
        </label>

        <button
          type="submit"
          disabled={connecting || handle.trim().length === 0}
          className="rounded-xl bg-guess px-4 py-4 text-lg font-bold text-black transition active:scale-[0.98] disabled:opacity-40"
        >
          {connecting ? "Connecting…" : "Join Game"}
        </button>

        {error && (
          <p className="text-center text-sm text-danger">{error}</p>
        )}
      </form>

      <p className="max-w-xs text-center text-xs leading-relaxed text-neutral-600">
        Your handle and a device ID are stored locally so you can resume your
        score if you drop out.
      </p>
    </div>
  );
}

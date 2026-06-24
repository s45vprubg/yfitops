// Resolves the backend endpoints. Defaults target a 127.0.0.1 dev backend;
// override with VITE_WT_URL / VITE_HTTP_URL at build/dev time.
//
// If the env vars are absent we derive from the current page host so the PWA
// works when served from the same machine on a LAN (phones can't resolve
// "localhost" pointing at the laptop).

function deriveHost(): string {
  if (typeof window !== "undefined" && window.location?.hostname) {
    return window.location.hostname;
  }
  return "127.0.0.1";
}

const host = deriveHost();

export const WT_URL: string =
  import.meta.env.VITE_WT_URL ?? `https://${host}:4433/wt`;

export const HTTP_URL: string =
  import.meta.env.VITE_HTTP_URL ?? `http://${host}:8777`;

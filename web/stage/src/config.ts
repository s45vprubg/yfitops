// Runtime configuration, env-overridable per the build contract.
// VITE_WT_URL    — WebTransport endpoint (default https://<host>:4433/wt)
// VITE_HTTP_URL  — plain-HTTP backend for cert-hash + Spotify OAuth (default http://<host>:8777)
// VITE_JOIN_URL  — mobile buzzer URL encoded in the QR (default http://<host>:5173)

const host = window.location.hostname || "127.0.0.1";

function env(key: string): string | undefined {
  const v = (import.meta.env as Record<string, string | undefined>)[key];
  return v && v.length > 0 ? v : undefined;
}

export const WT_URL = env("VITE_WT_URL") ?? `https://${host}:4433/wt`;
export const HTTP_URL = env("VITE_HTTP_URL") ?? `http://${host}:8777`;
// The mobile PWA's join URL. Falls back to the conventional mobile dev port.
export const JOIN_URL = env("VITE_JOIN_URL") ?? `http://${host}:5173`;

export const CERT_HASH_URL = `${HTTP_URL}/cert-hash`;
export const SPOTIFY_AUTH_URL = `${HTTP_URL}/auth/spotify`;
export const STAGE_SECRET = env("VITE_STAGE_SECRET") ?? "";

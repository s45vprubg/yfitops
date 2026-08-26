// Runtime configuration, env-overridable per the build contract.
// VITE_WT_URL    — WebTransport endpoint (default https://<host>:4433/wt)
// VITE_HTTP_URL  — plain-HTTP backend for cert-hash + Spotify OAuth (default http://<host>:8777)
// VITE_JOIN_URL  — mobile buzzer URL encoded in the QR (default http://<host>:8780)

const browserHost = window.location.hostname || "127.0.0.1";
const host = browserHost === "localhost" ? "127.0.0.1" : browserHost;

function env(key: string): string | undefined {
  const v = (import.meta.env as Record<string, string | undefined>)[key];
  return v && v.length > 0 ? v : undefined;
}

export const WT_URL = env("VITE_WT_URL") ?? `https://${host}:4433/wt`;
// Default the backend to https in production builds so the shared secret and
// cert-hash never traverse cleartext. In dev we keep plain http so `npm run
// dev` against the local stack works unchanged (§ dev http, no TLS).
export const HTTP_URL =
  env("VITE_HTTP_URL") ?? `${import.meta.env.DEV ? "http" : "https"}://${host}:8777`;
// The mobile PWA's join URL. Defaults to the mobile dev-server port (dev-up.sh).
export const JOIN_URL = env("VITE_JOIN_URL") ?? `http://${host}:8780`;

// Fail closed on a cleartext backend in production: the stage secret rides on
// the token/cert URLs as a Bearer, so http:// in a prod build would leak it.
// DEV is exempt so local http keeps working.
if (!import.meta.env.DEV && !HTTP_URL.startsWith("https://")) {
  throw new Error(
    `[stage] refusing to build token/cert URLs over cleartext HTTP_URL (${HTTP_URL}); ` +
      "set VITE_HTTP_URL to an https:// endpoint for production.",
  );
}

export const CERT_HASH_URL = `${HTTP_URL}/cert-hash`;
export const SPOTIFY_AUTH_URL = `${HTTP_URL}/auth/spotify`;
export const STAGE_SECRET = env("VITE_STAGE_SECRET") ?? "";

// SPOTIFY_TOKEN_URL serves a currently-valid access token (the backend
// refreshes server-side). The Stage's Web Playback SDK fetches from here on
// every getOAuthToken call so audio survives a multi-hour game (§6).
export const SPOTIFY_TOKEN_URL = `${HTTP_URL}/api/spotify/token`;

// fetchSpotifyToken pulls a live token from the backend, authenticating with
// the stage secret (same Bearer the /api routes require). Returns null on any
// failure so the caller can fall back to a cached token or demo mode.
export async function fetchSpotifyToken(): Promise<string | null> {
  try {
    const res = await fetch(SPOTIFY_TOKEN_URL, {
      headers: { Authorization: `Bearer ${STAGE_SECRET}` },
      cache: "no-store",
    });
    if (!res.ok) return null;
    const body = (await res.json()) as { token?: string | null; connected?: boolean };
    if (!body.connected || !body.token) return null;
    return body.token;
  } catch {
    return null;
  }
}

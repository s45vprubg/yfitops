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

// SPOTIFY_TOKEN_URL serves a currently-valid access token (the backend
// refreshes server-side). The Stage's Web Playback SDK fetches from here on
// every getOAuthToken call so audio survives a multi-hour game (§6).
export const SPOTIFY_TOKEN_URL = `${HTTP_URL}/api/spotify/token`;

// uis7-001: the result is tagged so callers can tell an auth rejection (the
// server positively said `secret` is wrong) apart from a genuine
// network/transport failure (unreachable, timeout, malformed response). These
// are NOT the same thing: a rejected secret must never cause the caller to
// fall back to a locally-cached token (that previously let a wrong secret
// still drive a real Spotify Connect device off a stale browser-cached
// token), whereas a network blip for an ALREADY-authenticated session should
// still be allowed to fall back, for resilience.
export type SpotifyTokenFetch =
  | { ok: true; token: string }
  | { ok: false; reason: "unauthorized" | "unavailable" };

// fetchSpotifyToken pulls a live token from the backend, authenticating with
// the stage secret (same Bearer the /api routes require). The secret is now
// entered at runtime via the stage login screen, not baked into the build
// (a build-time VITE_STAGE_SECRET auto-authenticated any visitor who loaded
// the page — see Login.tsx).
export async function fetchSpotifyToken(secret: string): Promise<SpotifyTokenFetch> {
  try {
    const res = await fetch(SPOTIFY_TOKEN_URL, {
      headers: { Authorization: `Bearer ${secret}` },
      cache: "no-store",
    });
    if (res.status === 401 || res.status === 403) {
      // The server positively rejected `secret` — distinct from "unreachable".
      return { ok: false, reason: "unauthorized" };
    }
    if (!res.ok) return { ok: false, reason: "unavailable" };
    const body = (await res.json()) as { token?: string | null; connected?: boolean };
    if (!body.connected || !body.token) return { ok: false, reason: "unavailable" };
    return { ok: true, token: body.token };
  } catch {
    // Network/transport failure (offline, timeout, DNS, etc.) — not an auth
    // rejection, so callers may still reasonably fall back to a cached token.
    return { ok: false, reason: "unavailable" };
  }
}

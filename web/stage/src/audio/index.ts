// Audio layer factory + token plumbing.
//
// OAuth flow (design_doc §6): an operator clicks "Connect Spotify", which sends
// the tab to `${HTTP_URL}/auth/spotify`. The backend runs the OAuth dance and
// redirects back to the stage with an access token. We accept the token from
// either:
//   - URL hash:    #access_token=...&expires_in=...
//   - URL query:   ?access_token=...
//   - localStorage (yfitops.spotifyToken) — persisted across reloads
//
// If a usable token is found we build a SpotifyAudioPlayer; otherwise we run the
// MockAudioPlayer (demo mode). Nothing else in the app cares which it gets.

import type { AudioPlayer } from "./types";
import { MockAudioPlayer } from "./mock";
import { SpotifyAudioPlayer } from "./spotify";
import { fetchSpotifyToken } from "../config";

const TOKEN_KEY = "yfitops.spotifyToken";
const TOKEN_EXP_KEY = "yfitops.spotifyTokenExp";

function readTokenFromLocation(): { token: string; expiresInSec: number } | null {
  const sources = [window.location.hash.replace(/^#/, ""), window.location.search.replace(/^\?/, "")];
  for (const raw of sources) {
    if (!raw) continue;
    const params = new URLSearchParams(raw);
    const token = params.get("access_token");
    if (token) {
      const expiresIn = Number(params.get("expires_in") ?? "3600");
      return { token, expiresInSec: Number.isFinite(expiresIn) ? expiresIn : 3600 };
    }
  }
  return null;
}

function persistToken(token: string, expiresInSec: number) {
  try {
    localStorage.setItem(TOKEN_KEY, token);
    localStorage.setItem(TOKEN_EXP_KEY, String(Date.now() + expiresInSec * 1000));
  } catch {
    /* private mode — ignore */
  }
}

function readPersistedToken(): string | null {
  try {
    const token = localStorage.getItem(TOKEN_KEY);
    const exp = Number(localStorage.getItem(TOKEN_EXP_KEY) ?? "0");
    if (token && exp > Date.now()) return token;
  } catch {
    /* ignore */
  }
  return null;
}

/** Capture a token from the OAuth redirect and strip it from the URL bar. */
export function captureSpotifyToken(): string | null {
  const fromLocation = readTokenFromLocation();
  if (fromLocation) {
    persistToken(fromLocation.token, fromLocation.expiresInSec);
    // Scrub the token out of the address bar.
    history.replaceState(null, "", window.location.pathname + window.location.search.replace(/access_token=[^&]*&?/g, ""));
    if (window.location.hash) history.replaceState(null, "", window.location.pathname);
    return fromLocation.token;
  }
  return readPersistedToken();
}

// liveTokenProvider returns a token for the SDK's getOAuthToken callback. It
// prefers a freshly-refreshed token from the backend (so audio survives the
// ~1h Spotify token TTL across a long game), falling back to the cached token
// from the OAuth redirect if the endpoint is unreachable.
async function liveTokenProvider(initial: string): Promise<string> {
  const fresh = await fetchSpotifyToken();
  return fresh ?? readPersistedToken() ?? initial;
}

/** Build the right player. Returns a Spotify player if a token exists, else mock. */
export function createAudioPlayer(): AudioPlayer {
  const token = captureSpotifyToken();
  if (token) {
    return new SpotifyAudioPlayer(() => liveTokenProvider(token));
  }
  return new MockAudioPlayer();
}

export function hasSpotifyToken(): boolean {
  return captureSpotifyToken() !== null;
}

export { SpotifyAudioPlayer } from "./spotify";
export type { AudioPlayer } from "./types";
export type { PlayerState, ConnectState, AudioMode } from "./types";

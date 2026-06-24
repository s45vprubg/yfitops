// Fetches the dev self-signed cert SHA-256 hash from the backend so the browser
// WebTransport API can trust it without a CA (serverCertificateHashes).
//
// The backend exposes it at `${HTTP_URL}/cert-hash`. We accept either a hex
// string ("ab:cd:..." or "abcd...") or base64. Returns the structure the
// GameClient's ClientOptions.serverCertHashes expects.

import { CERT_HASH_URL } from "../config";

function hexToBytes(hex: string): Uint8Array {
  const clean = hex.replace(/[^0-9a-fA-F]/g, "");
  const out = new Uint8Array(clean.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(clean.substr(i * 2, 2), 16);
  }
  return out;
}

function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64.replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function looksHex(s: string): boolean {
  const clean = s.replace(/[^0-9a-fA-F]/g, "");
  return clean.length === 64; // SHA-256 = 32 bytes = 64 hex chars
}

export type CertHashes = { algorithm: "sha-256"; value: BufferSource }[];

export async function fetchCertHashes(): Promise<CertHashes | undefined> {
  try {
    const res = await fetch(CERT_HASH_URL, { mode: "cors" });
    if (!res.ok) return undefined;
    const text = (await res.text()).trim();
    if (!text) return undefined;

    // Try JSON wrapper { hash: "..." } first, then raw string.
    let raw = text;
    try {
      const j = JSON.parse(text);
      if (typeof j === "string") raw = j;
      else if (j && typeof j.hash === "string") raw = j.hash;
    } catch {
      /* not JSON — use as-is */
    }

    const bytes = looksHex(raw) ? hexToBytes(raw) : base64ToBytes(raw);
    if (bytes.length !== 32) return undefined;
    return [{ algorithm: "sha-256", value: bytes }];
  } catch {
    // No backend / CORS blocked — connect without pinned hashes (prod CA case).
    return undefined;
  }
}

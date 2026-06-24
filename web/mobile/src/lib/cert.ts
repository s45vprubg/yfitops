// Dev self-signed cert support. The backend exposes the base64 SHA-256 hash of
// its cert at GET <HTTP_URL>/cert-hash. WebTransport's serverCertificateHashes
// lets the browser accept that cert without a CA — but only for short-lived
// certs (<14 days). In production with a real cert, this fetch is skipped.

import { HTTP_URL } from "./env";

function base64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64.trim());
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

export async function fetchCertHashes(): Promise<
  { algorithm: "sha-256"; value: BufferSource }[] | undefined
> {
  try {
    const res = await fetch(`${HTTP_URL}/cert-hash`, { cache: "no-store" });
    if (!res.ok) return undefined;
    const b64 = (await res.text()).trim();
    if (!b64) return undefined;
    return [
      { algorithm: "sha-256", value: base64ToBytes(b64) as BufferSource },
    ];
  } catch {
    // No dev cert endpoint reachable (e.g. prod CA cert) — connect plainly.
    return undefined;
  }
}

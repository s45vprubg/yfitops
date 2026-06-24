// Environment-configurable endpoints (design_doc §9 / build prompt).
//   VITE_WT_URL   — WebTransport endpoint, e.g. https://host:4433/wt
//   VITE_HTTP_URL — plain HTTP base for the dev cert-hash endpoint, e.g. http://host:8777
//
// Defaults assume the backend runs on the same host the dashboard is served from.

const host = typeof location !== "undefined" ? location.hostname || "127.0.0.1" : "127.0.0.1";

export const WT_URL = import.meta.env.VITE_WT_URL ?? `https://${host}:4433/wt`;
export const HTTP_URL = import.meta.env.VITE_HTTP_URL ?? `http://${host}:8777`;

// Fetch the dev self-signed cert SHA-256 hash so the browser will accept the
// WebTransport connection without a CA (serverCertificateHashes). The dev
// server (main.go) exposes it at GET /cert-hash as standard base64. We also
// tolerate a hex string so a differently-configured backend still works.
export async function fetchCertHash(): Promise<
  { algorithm: "sha-256"; value: Uint8Array }[] | undefined
> {
  try {
    const res = await fetch(`${HTTP_URL}/cert-hash`, { cache: "no-store" });
    if (!res.ok) return undefined;
    const text = (await res.text()).trim().replace(/^sha-?256:/i, "");

    // Hex form (64 hex chars)?
    const hex = text.replace(/[^0-9a-fA-F]/g, "");
    if (hex.length === 64 && /^[0-9a-fA-F]+$/.test(text.replace(/:/g, ""))) {
      const bytes = new Uint8Array(32);
      for (let i = 0; i < 32; i++) {
        bytes[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
      }
      return [{ algorithm: "sha-256", value: bytes }];
    }

    // Otherwise treat as base64 (what the backend actually sends).
    const bin = atob(text);
    if (bin.length !== 32) return undefined;
    const bytes = new Uint8Array(32);
    for (let i = 0; i < 32; i++) bytes[i] = bin.charCodeAt(i);
    return [{ algorithm: "sha-256", value: bytes }];
  } catch {
    // In production behind a real CA the hash is unnecessary; ignore failures.
    return undefined;
  }
}

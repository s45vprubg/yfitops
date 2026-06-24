// client.ts — shared WebTransport client used by all three frontends.
// FIXED CONTRACT for the frontends. Wraps the browser WebTransport API
// (design_doc §2 — WebTransport over QUIC/HTTP3) with a length-prefixed JSON
// framing over a single bidirectional control stream.
//
// Framing: each frame is [4-byte big-endian length][UTF-8 JSON envelope].
// This keeps the door open for binary later (§11) while staying debuggable.
//
// The server mirrors this framing in internal/transport.

import type { ClientEnvelope, ServerEnvelope, ServerMsgType } from "./protocol";

type Handler = (env: ServerEnvelope) => void;

export interface ClientOptions {
  url: string; // e.g. https://host:4433/wt
  // serverCertHashes: for dev self-signed certs, pass the SHA-256 hash so the
  // browser accepts the cert without a CA (WebTransport serverCertificateHashes).
  serverCertHashes?: { algorithm: "sha-256"; value: BufferSource }[];
  onState?: (connected: boolean) => void;
}

export class GameClient {
  private wt?: WebTransport;
  private writer?: WritableStreamDefaultWriter<Uint8Array>;
  private handlers = new Map<ServerMsgType, Set<Handler>>();
  private anyHandlers = new Set<Handler>();
  private lastNonce = 0;
  private opts: ClientOptions;
  private closed = false;

  constructor(opts: ClientOptions) {
    this.opts = opts;
  }

  get nonce(): number {
    return this.lastNonce;
  }

  async connect(): Promise<void> {
    const init: WebTransportOptions = {};
    if (this.opts.serverCertHashes) {
      init.serverCertificateHashes = this.opts.serverCertHashes;
    }
    this.wt = new WebTransport(this.opts.url, init);
    await this.wt.ready;
    this.opts.onState?.(true);

    const stream = await this.wt.createBidirectionalStream();
    this.writer = stream.writable.getWriter();
    this.readLoop(stream.readable.getReader()).catch(() => this.handleClose());
    this.wt.closed.then(() => this.handleClose()).catch(() => this.handleClose());
  }

  on(type: ServerMsgType, h: Handler): () => void {
    let set = this.handlers.get(type);
    if (!set) {
      set = new Set();
      this.handlers.set(type, set);
    }
    set.add(h);
    return () => set!.delete(h);
  }

  onAny(h: Handler): () => void {
    this.anyHandlers.add(h);
    return () => this.anyHandlers.delete(h);
  }

  async send<D>(env: ClientEnvelope<D>): Promise<void> {
    if (!this.writer) throw new Error("not connected");
    // Always stamp the latest observed nonce unless the caller set one.
    if (env.n === undefined) env.n = this.lastNonce;
    const json = JSON.stringify(env);
    const body = new TextEncoder().encode(json);
    const frame = new Uint8Array(4 + body.length);
    new DataView(frame.buffer).setUint32(0, body.length, false);
    frame.set(body, 4);
    await this.writer.write(frame);
  }

  async close(): Promise<void> {
    this.closed = true;
    try {
      await this.writer?.close();
    } catch {
      /* ignore */
    }
    try {
      this.wt?.close();
    } catch {
      /* ignore */
    }
  }

  private handleClose() {
    if (this.closed) return;
    this.opts.onState?.(false);
  }

  private async readLoop(reader: ReadableStreamDefaultReader<Uint8Array>) {
    let buf = new Uint8Array(0);
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      if (!value) continue;
      buf = concat(buf, value);
      // Drain as many complete frames as are buffered.
      for (;;) {
        if (buf.length < 4) break;
        const len = new DataView(buf.buffer, buf.byteOffset, 4).getUint32(0, false);
        if (buf.length < 4 + len) break;
        const body = buf.subarray(4, 4 + len);
        buf = buf.subarray(4 + len);
        try {
          const env = JSON.parse(new TextDecoder().decode(body)) as ServerEnvelope;
          this.dispatch(env);
        } catch {
          /* malformed frame — skip */
        }
      }
    }
    this.handleClose();
  }

  private dispatch(env: ServerEnvelope) {
    if (typeof env.n === "number" && env.n > 0) this.lastNonce = env.n;
    this.anyHandlers.forEach((h) => h(env));
    this.handlers.get(env.t)?.forEach((h) => h(env));
  }
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}

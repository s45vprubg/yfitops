// client.ts — shared WebTransport client used by all three frontends.
// FIXED CONTRACT for the frontends. Wraps the browser WebTransport API
// (design_doc §2 — WebTransport over QUIC/HTTP3) with a length-prefixed JSON
// framing over a single bidirectional control stream.
//
// Framing: each frame is [4-byte big-endian length][UTF-8 JSON envelope].
// This keeps the door open for binary later (§11) while staying debuggable.
//
// The server mirrors this framing in internal/transport.
//
// FALLBACK: When WebTransport is unavailable (non-secure context, e.g. phone
// over plain HTTP on a LAN), the client falls back to a WebSocket connection
// with the same length-prefixed binary framing. The server only exposes the
// /ws endpoint when YFI_DEV_WS=1 — this fallback is dev-only.

// MAX_FRAME_LEN mirrors the server's maxFrameLen (internal/transport/framing.go,
// 1<<20). Frames larger than this are a protocol violation and are refused
// rather than buffered (see readLoop).
const MAX_FRAME_LEN = 1 << 20;

import type { ClientEnvelope, ServerEnvelope, ServerMsgType } from "./protocol";

type Handler = (env: ServerEnvelope) => void;

export interface ClientOptions {
  url: string; // e.g. https://host:4433/wt
  // wsUrl: WebSocket fallback URL (e.g. ws://host:8777/ws). Only used when
  // WebTransport is not available (non-secure context).
  wsUrl?: string;
  // serverCertHashes: for dev self-signed certs, pass the SHA-256 hash so the
  // browser accepts the cert without a CA (WebTransport serverCertificateHashes).
  serverCertHashes?: { algorithm: "sha-256"; value: BufferSource }[];
  onState?: (connected: boolean) => void;
}

export class GameClient {
  private wt?: WebTransport;
  private ws?: WebSocket;
  private writer?: WritableStreamDefaultWriter<Uint8Array>;
  private handlers = new Map<ServerMsgType, Set<Handler>>();
  private anyHandlers = new Set<Handler>();
  private lastNonce = 0;
  private opts: ClientOptions;
  private closed = false;
  // CONTRACT-QUESTION (QA sweep s4-ui-02/s4-ui-new-02): single-fire teardown
  // latch. A single teardown event used to fire handleClose() TWICE on both
  // transports — feedWS does ws.close(1009) *and* handleClose() (and the close
  // re-enters via ws.onclose), readLoopWT does reader.cancel() *and*
  // handleClose() (and ending the session re-enters via wt.closed). Callers
  // build a fresh GameClient per connection attempt, so a permanent latch is
  // safe; if an internal reconnect is ever added, reset `down` in connect().
  private down = false;

  constructor(opts: ClientOptions) {
    this.opts = opts;
  }

  get nonce(): number {
    return this.lastNonce;
  }

  async connect(): Promise<void> {
    if (typeof WebTransport !== "undefined") {
      await this.connectWT();
    } else if (this.opts.wsUrl) {
      await this.connectWS();
    } else {
      throw new Error("WebTransport unavailable and no WebSocket fallback URL configured");
    }
  }

  private async connectWT(): Promise<void> {
    const init: WebTransportOptions = {};
    if (this.opts.serverCertHashes) {
      init.serverCertificateHashes = this.opts.serverCertHashes;
    }
    this.wt = new WebTransport(this.opts.url, init);
    await this.wt.ready;
    this.opts.onState?.(true);

    const stream = await this.wt.createBidirectionalStream();
    this.writer = stream.writable.getWriter();
    this.readLoopWT(stream.readable.getReader()).catch(() => this.handleClose());
    this.wt.closed.then(() => this.handleClose()).catch(() => this.handleClose());
  }

  private connectWS(): Promise<void> {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(this.opts.wsUrl!);
      ws.binaryType = "arraybuffer";
      this.ws = ws;

      let opened = false;

      ws.onopen = () => {
        opened = true;
        this.opts.onState?.(true);
        resolve();
      };
      ws.onerror = () => {
        // CONTRACT-QUESTION (QA sweep s4-ui-02): a failed handshake must report
        // ONCE. connectWS wires onclose at construction time (connectWT only
        // chains teardown after wt.ready resolves), so pre-open the caller used
        // to get BOTH a connect() rejection and an onState(false). Latch the
        // teardown here so the rejection is the single signal — matching
        // connectWT. Only pre-open: an error on a live socket must still let
        // onclose report the disconnect, or the reconnect loop wedges.
        if (!opened) this.down = true;
        reject(new Error("WebSocket connection failed"));
      };
      ws.onclose = () => this.handleClose();
      ws.onmessage = (ev) => {
        // CONTRACT-QUESTION (QA sweep s4-ui-new-03): the framing is binary-only.
        // An unchecked `as ArrayBuffer` cast made a TEXT frame become a
        // zero-length Uint8Array — the frame vanished with no throw and no
        // teardown. Fail loudly instead (hardening: this server only ever
        // writes binary; a proxy or hand-rolled dev server might not).
        if (!(ev.data instanceof ArrayBuffer)) {
          try {
            ws.close(1003, "binary frames only");
          } catch {
            /* ignore */
          }
          this.handleClose();
          return;
        }
        this.feedWS(new Uint8Array(ev.data));
      };
    });
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
    if (env.n === undefined) env.n = this.lastNonce;
    const json = JSON.stringify(env);
    const body = new TextEncoder().encode(json);
    const frame = new Uint8Array(4 + body.length);
    new DataView(frame.buffer).setUint32(0, body.length, false);
    frame.set(body, 4);

    if (this.writer) {
      await this.writer.write(frame);
    } else if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(frame);
    } else {
      throw new Error("not connected");
    }
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
    try {
      this.ws?.close();
    } catch {
      /* ignore */
    }
  }

  private handleClose() {
    if (this.closed || this.down) return;
    this.down = true;
    this.opts.onState?.(false);
  }

  // --- WebTransport read loop ---
  private async readLoopWT(reader: ReadableStreamDefaultReader<Uint8Array>) {
    let buf = new Uint8Array(0);
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      if (!value) continue;
      buf = concat(buf, value);
      const drained = this.drainFrames(buf);
      if (drained === null) {
        // Oversized frame — protocol violation. Tear the stream down.
        reader.cancel("frame too large").catch(() => {});
        this.handleClose();
        return;
      }
      buf = drained;
    }
    this.handleClose();
  }

  // --- WebSocket message accumulator ---
  private wsBuf = new Uint8Array(0);

  private feedWS(data: Uint8Array) {
    this.wsBuf = concat(this.wsBuf, data);
    const drained = this.drainFrames(this.wsBuf);
    if (drained === null) {
      // Oversized frame — protocol violation. Tear the socket down.
      this.wsBuf = new Uint8Array(0);
      try {
        this.ws?.close(1009, "frame too large");
      } catch {
        /* ignore */
      }
      this.handleClose();
      return;
    }
    this.wsBuf = drained;
  }

  // --- Shared frame parser ---
  // Drains as many complete frames as are buffered, dispatching each. Returns
  // the unconsumed remainder, or null if the peer sent an oversized frame
  // (protocol violation — the caller must tear its transport down).
  private drainFrames(buf: Uint8Array): Uint8Array | null {
    for (;;) {
      if (buf.length < 4) break;
      const len = new DataView(buf.buffer, buf.byteOffset, 4).getUint32(0, false);
      // CONTRACT-QUESTION (QA sweep uiadmin-2): mirror the server's 1 MiB
      // maxFrameLen (internal/transport/framing.go). Without this cap a corrupt
      // or hostile 4-byte prefix (e.g. 0xFFFFFFFF) makes the client buffer up to
      // ~4 GiB before dispatch — an unbounded-memory hang. A frame over the cap
      // is a protocol violation; tear the transport down rather than buffer it.
      if (len > MAX_FRAME_LEN) return null;
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
    return buf;
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

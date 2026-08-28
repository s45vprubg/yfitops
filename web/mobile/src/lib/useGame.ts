import { useCallback, useEffect, useRef, useState } from "react";
import { GameClient } from "@shared/client";
import type {
  GameState,
  HelloData,
  MaskedRevealData,
  RateData,
  ScoreboardData,
  ServerEnvelope,
  StateData,
  LockoutData,
  BuzzResultData,
  VoteStateData,
  WelcomeData,
  ErrorData,
} from "@shared/protocol";
import { WT_URL, WS_URL } from "./env";
import { fetchCertHashes } from "./cert";
import { getDeviceFP, saveHandle } from "./fingerprint";

// Connection lifecycle, separate from game state so the UI can show a
// reconnecting banner without losing the last-known screen.
export type ConnStatus = "idle" | "connecting" | "connected" | "disconnected";

// All state below is derived PURELY from server flags + sanitized payloads.
// The ONE answer-derived exception is `maskedReveal` (§4A extension): the
// server-driven letter reveal, which carries only letters ALREADY shown on the
// projector in the same broadcast — a phone can never learn a letter early. It
// never carries the full title/artist/uri/lyrics; hidden slots are blank.
export interface GameView {
  conn: ConnStatus;
  joined: boolean;
  state: GameState;
  // Who is currently guessing (from lockout payload), if any.
  lockedBy: string | null;
  // This device's own buzz was rejected (lost the race or guessed wrong).
  buzzedAndLost: boolean;
  // This player won the buzz this round (used to detect post-adjudication lockout).
  wonBuzzThisRound: boolean;
  // This player was judged incorrect/partial and is locked out for the rest of the round.
  judgedThisRound: boolean;
  // The verdict received after adjudication ("partial" or "incorrect").
  lastVerdict: "partial" | "incorrect" | null;
  // Vote progress during KARAOKE.
  vote: VoteStateData | null;
  // Server-authoritative letter reveal (only stage-visible letters; see above).
  maskedReveal: MaskedRevealData | null;
  // Live standings (handles + scores) so players see their rank.
  scoreboard: ScoreboardData | null;
  // Most recent server error message (e.g. bad nonce, kicked).
  error: string | null;
  rttMs: number | null;
}

const HEARTBEAT_MS = 2000;
// Auto-reconnect backoff: doubles from base up to the cap (§3.2 resume).
const RECONNECT_BASE_MS = 500;
const RECONNECT_MAX_MS = 8000;

const INITIAL: GameView = {
  conn: "idle",
  joined: false,
  state: "LOBBY",
  lockedBy: null,
  buzzedAndLost: false,
  wonBuzzThisRound: false,
  judgedThisRound: false,
  lastVerdict: null,
  vote: null,
  maskedReveal: null,
  scoreboard: null,
  error: null,
  rttMs: null,
};

export function useGame() {
  const [view, setView] = useState<GameView>(INITIAL);
  const clientRef = useRef<GameClient | null>(null);
  const heartbeatRef = useRef<ReturnType<typeof setInterval> | null>(null);
  // Maps the clientTime we stamped to compute RTT when the echo returns.
  const pendingPing = useRef<number | null>(null);
  // True between sending a buzz and receiving the buzzResult response.
  const awaitingBuzzResult = useRef(false);
  // Reconnect bookkeeping (§3.2 resume). We keep the handle + deviceFP stable
  // across drops so the server re-attaches this device to its saved score.
  const unmountedRef = useRef(false);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reconnectAttemptRef = useRef(0);
  const handleRef = useRef<string>("");
  // Holds the latest establish() so scheduleReconnect can call it without a
  // useCallback dependency cycle (establish -> handleDisconnect -> schedule).
  const establishRef = useRef<
    (handle: string, isReconnect: boolean) => void
  >(() => {});

  const patch = useCallback((p: Partial<GameView>) => {
    setView((v) => ({ ...v, ...p }));
  }, []);

  const lastRtt = useRef<number>(0);

  const sendHeartbeat = useCallback(async () => {
    const c = clientRef.current;
    if (!c) return;
    const clientTime = Date.now();
    pendingPing.current = clientTime;
    // Awaited/caught so a write on a dead stream doesn't become an unhandled
    // rejection every HEARTBEAT_MS; onState(false) will tear the interval down.
    try {
      await c.send({ t: "heartbeat", d: { clientTime, rttMs: lastRtt.current } });
    } catch {
      /* stream is gone; disconnect handler will clean up */
    }
  }, []);

  const stopHeartbeat = useCallback(() => {
    if (heartbeatRef.current) {
      clearInterval(heartbeatRef.current);
      heartbeatRef.current = null;
    }
  }, []);

  const startHeartbeat = useCallback(() => {
    stopHeartbeat();
    void sendHeartbeat();
    heartbeatRef.current = setInterval(() => void sendHeartbeat(), HEARTBEAT_MS);
  }, [sendHeartbeat, stopHeartbeat]);

  const wireHandlers = useCallback(
    (c: GameClient) => {
      c.on("welcome", (env: ServerEnvelope) => {
        const d = env.d as WelcomeData | undefined;
        if (d) patch({ joined: true, error: null });
      });

      c.on("state", (env: ServerEnvelope) => {
        const d = env.d as StateData | undefined;
        if (!d) return;
        setView((v) => {
          const next: Partial<GameView> = { state: d.state };
          if (d.state === "ROUND_ACTIVE") {
            if (v.state === "ADJUDICATE" && v.wonBuzzThisRound) {
              // We were the guesser and got judged incorrect/partial.
              next.judgedThisRound = true;
              next.buzzedAndLost = true;
              next.wonBuzzThisRound = false;
            } else if (v.state === "ADJUDICATE") {
              // Someone else was judged — re-enable our buzzer.
              next.buzzedAndLost = false;
              next.lockedBy = null;
              next.wonBuzzThisRound = false;
            } else {
              // Fresh round (from BOARD/TRANSITION) — reset everything.
              next.buzzedAndLost = false;
              next.judgedThisRound = false;
              next.wonBuzzThisRound = false;
              next.lockedBy = null;
              next.lastVerdict = null;
              // A stale buzzResult from a prior round must not spuriously flip
              // buzzedAndLost now (uimobile-5).
              awaitingBuzzResult.current = false;
            }
          }
          if (d.state === "BOARD" || d.state === "LOBBY" || d.state === "TRANSITION") {
            next.buzzedAndLost = false;
            next.judgedThisRound = false;
            next.wonBuzzThisRound = false;
            next.lockedBy = null;
            next.maskedReveal = null;
            next.lastVerdict = null;
          }
          if (d.state !== "LOCKED_OUT") {
            next.lockedBy = d.state === "ROUND_ACTIVE" ? (next.lockedBy ?? null) : v.lockedBy;
          }
          if (d.state !== "KARAOKE") next.vote = null;
          return { ...v, ...next };
        });
      });

      c.on("lockout", (env: ServerEnvelope) => {
        const d = env.d as LockoutData | undefined;
        patch({ lockedBy: d?.byHandle ?? "another player" });
      });

      c.on("buzzResult", (env: ServerEnvelope) => {
        const d = env.d as BuzzResultData | undefined;
        if (!d) return;
        if (d.won) {
          awaitingBuzzResult.current = false;
          patch({ wonBuzzThisRound: true });
        } else if (awaitingBuzzResult.current) {
          awaitingBuzzResult.current = false;
          patch({ buzzedAndLost: true });
        }
      });

      c.on("gradeResult", (env: ServerEnvelope) => {
        const d = env.d as { verdict: string } | undefined;
        if (d) patch({ lastVerdict: d.verdict as "partial" | "incorrect" });
      });

      c.on("voteState", (env: ServerEnvelope) => {
        const d = env.d as VoteStateData | undefined;
        if (d) patch({ vote: d });
      });

      // Server-authoritative letter reveal (§4A extension). Only letters already
      // shown on the projector in the same broadcast ever arrive here.
      c.on("maskedReveal", (env: ServerEnvelope) => {
        const d = env.d as MaskedRevealData | undefined;
        if (d) patch({ maskedReveal: d });
      });

      c.on("scoreboard", (env: ServerEnvelope) => {
        const d = env.d as ScoreboardData | undefined;
        if (d) patch({ scoreboard: d });
      });

      c.on("heartbeat", () => {
        if (pendingPing.current != null) {
          const rtt = Date.now() - pendingPing.current;
          pendingPing.current = null;
          // Ignore an echo that arrives after a stall/recover — the elapsed
          // time reflects the gap, not the link RTT, and would be fed back to
          // the server (§4B) as a bogus latency sample.
          if (rtt <= HEARTBEAT_MS * 5) {
            lastRtt.current = rtt;
            patch({ rttMs: rtt });
          }
        }
      });

      c.on("error", (env: ServerEnvelope) => {
        const d = env.d as ErrorData | undefined;
        patch({ error: d?.message ?? "Server error" });
      });
    },
    [patch],
  );

  // Schedule a reconnect attempt with capped exponential backoff. Guards
  // against overlapping schedules and against firing after unmount.
  const scheduleReconnect = useCallback(() => {
    if (unmountedRef.current) return;
    if (reconnectTimerRef.current) return;
    const attempt = reconnectAttemptRef.current;
    const delay = Math.min(RECONNECT_BASE_MS * 2 ** attempt, RECONNECT_MAX_MS);
    reconnectAttemptRef.current = attempt + 1;
    reconnectTimerRef.current = setTimeout(() => {
      reconnectTimerRef.current = null;
      if (unmountedRef.current || clientRef.current) return;
      establishRef.current(handleRef.current, true);
    }, delay);
  }, []);

  // Runs when the transport drops on its own (GameClient onState(false)) — NOT
  // on our own close(), which sets client.closed and suppresses this callback.
  // Tears down the dead client so a fresh connect() can proceed (uimobile-1),
  // stops the heartbeat so it can't write to a dead stream (uimobile-2), and
  // clears the RTT/buzz latches that would otherwise go stale (uimobile-5/6).
  const handleDisconnect = useCallback(() => {
    stopHeartbeat();
    pendingPing.current = null;
    awaitingBuzzResult.current = false;
    // Close the dead client before dropping the ref so its own handleClose can't
    // fire another onState(false) later (close() sets client.closed) — otherwise
    // a lingering QUIC session leaks and churns reconnects (s2-ui-02).
    const prev = clientRef.current;
    clientRef.current = null;
    void prev?.close();
    if (unmountedRef.current) return;
    patch({ conn: "disconnected" });
    scheduleReconnect();
  }, [patch, scheduleReconnect, stopHeartbeat]);

  const establish = useCallback(
    async (handle: string, isReconnect: boolean) => {
      if (clientRef.current) return;
      handleRef.current = handle;
      patch({ conn: "connecting", error: null });
      saveHandle(handle);

      let client: GameClient;
      try {
        const serverCertHashes = await fetchCertHashes();
        client = new GameClient({
          url: WT_URL,
          // DEV-ONLY WebSocket fallback for phones on a plain-HTTP LAN origin,
          // where WebTransport is unavailable (non-secure context). Gated on
          // import.meta.env.DEV so a production bundle has no cleartext ws://
          // path at all — the server likewise refuses /ws when YFI_ENV=prod.
          // In prod the fix is TLS + WebTransport, not this.
          wsUrl: import.meta.env.DEV ? WS_URL : undefined,
          serverCertHashes,
          onState: (connected) => {
            // Ignore state events from a superseded client (s2-ui-02): a stale
            // client's late onState(false) must not disconnect a healthy newer
            // one. close() sets client.closed so the intended path stays quiet.
            if (client !== clientRef.current) return;
            if (connected) {
              reconnectAttemptRef.current = 0;
              patch({ conn: "connected" });
            } else {
              handleDisconnect();
            }
          },
        });
        // establish() early-returns if clientRef.current is set, so the old
        // client is always torn down (and close()d) by handleDisconnect before
        // we get here — no previous client to close on this path (s2-ui-02).
        clientRef.current = client;
        wireHandlers(client);
        await client.connect();
      } catch (e) {
        patch({
          conn: "disconnected",
          error: e instanceof Error ? e.message : "Connection failed",
        });
        // Close the half-open client, don't just drop it (s4-ui-new-01):
        // connectWT awaits wt.ready BEFORE createBidirectionalStream, so if
        // anything after ready throws, the QUIC session is OPEN and this was its
        // only reference — every backoff retry would leak another live session
        // and burn a per-IP limiter slot. Captured before nulling the ref (the
        // local `client` may be unassigned if fetchCertHashes threw), same
        // prev/null/close order as handleDisconnect.
        const dead = clientRef.current;
        clientRef.current = null;
        void dead?.close();
        // Keep trying — a mid-game drop must not permanently wedge the player.
        scheduleReconnect();
        return;
      }

      // Re-attach with the SAME deviceFP + saved handle so the server resumes
      // this device's score (§3.2), then ask for a fresh state snapshot.
      // Guarded (s2-ui-03): a write-side reject here must not become an
      // unhandled rejection that strands clientRef non-null with no heartbeat
      // and no reconnect — mirror the connect() catch.
      try {
        await client.send<HelloData>({
          t: "hello",
          d: { role: "mobile", handle, deviceFP: getDeviceFP() },
        });
        if (isReconnect) {
          await client.send({ t: "resync" });
        }
        startHeartbeat();
      } catch (e) {
        patch({
          conn: "disconnected",
          error: e instanceof Error ? e.message : "Connection failed",
        });
        clientRef.current = null;
        void client.close();
        scheduleReconnect();
        return;
      }
    },
    [handleDisconnect, patch, scheduleReconnect, startHeartbeat, wireHandlers],
  );

  useEffect(() => {
    establishRef.current = (handle, isReconnect) =>
      void establish(handle, isReconnect);
  }, [establish]);

  const connect = useCallback(
    (handle: string) => void establish(handle, false),
    [establish],
  );

  const buzz = useCallback(() => {
    const c = clientRef.current;
    if (!c) return;
    awaitingBuzzResult.current = true;
    void c.send({ t: "buzz" });
  }, []);

  const vote = useCallback(() => {
    const c = clientRef.current;
    if (!c) return;
    void c.send({ t: "vote" });
  }, []);

  const rate = useCallback((stars: number) => {
    const c = clientRef.current;
    if (!c) return;
    void c.send<RateData>({ t: "rate", d: { stars } });
  }, []);

  useEffect(() => {
    return () => {
      unmountedRef.current = true;
      if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current);
      if (heartbeatRef.current) clearInterval(heartbeatRef.current);
      void clientRef.current?.close();
    };
  }, []);

  return { view, connect, buzz, vote, rate };
}

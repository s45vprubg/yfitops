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
import { WT_URL } from "./env";
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

  const patch = useCallback((p: Partial<GameView>) => {
    setView((v) => ({ ...v, ...p }));
  }, []);

  const lastRtt = useRef<number>(0);

  const sendHeartbeat = useCallback(() => {
    const c = clientRef.current;
    if (!c) return;
    const clientTime = Date.now();
    pendingPing.current = clientTime;
    void c.send({ t: "heartbeat", d: { clientTime, rttMs: lastRtt.current } });
  }, []);

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
          lastRtt.current = rtt;
          patch({ rttMs: rtt });
          pendingPing.current = null;
        }
      });

      c.on("error", (env: ServerEnvelope) => {
        const d = env.d as ErrorData | undefined;
        patch({ error: d?.message ?? "Server error" });
      });
    },
    [patch],
  );

  const connect = useCallback(
    async (handle: string) => {
      if (clientRef.current) return;
      patch({ conn: "connecting", error: null });
      saveHandle(handle);

      const serverCertHashes = await fetchCertHashes();
      const client = new GameClient({
        url: WT_URL,
        serverCertHashes,
        onState: (connected) =>
          patch({ conn: connected ? "connected" : "disconnected" }),
      });
      clientRef.current = client;
      wireHandlers(client);

      try {
        await client.connect();
      } catch (e) {
        patch({
          conn: "disconnected",
          error: e instanceof Error ? e.message : "Connection failed",
        });
        clientRef.current = null;
        return;
      }

      await client.send<HelloData>({
        t: "hello",
        d: { role: "mobile", handle, deviceFP: getDeviceFP() },
      });

      sendHeartbeat();
      heartbeatRef.current = setInterval(sendHeartbeat, HEARTBEAT_MS);
    },
    [patch, sendHeartbeat, wireHandlers],
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
      if (heartbeatRef.current) clearInterval(heartbeatRef.current);
      void clientRef.current?.close();
    };
  }, []);

  return { view, connect, buzz, vote, rate };
}

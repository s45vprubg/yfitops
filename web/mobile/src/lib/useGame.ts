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
  DailyDoublePerformerData,
  DailyDoubleSongChoice,
  DailyDoubleResultData,
  DailyDoubleDecisionData,
  DailyDoubleChooseData,
  PickerStatusData,
  BoardData,
  PlayerSelectCellData,
  SoloKaraokeActiveData,
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
  // True once a "kicked" error (admin Telemetry panel's Kick button) has
  // been latched. Distinct from a routine disconnect: no auto-reconnect is
  // scheduled while this is true, and App.tsx renders an explicit, unmissable
  // screen instead of silently reverting to the bare join form. Cleared the
  // moment the player manually rejoins (see `connect`).
  kicked: boolean;
  rttMs: number | null;
  // This device's own player ID (from welcome), used to tell whether WE are
  // the Daily Double contestant.
  myPlayerID: string | null;
  // Who's up for a Daily Double (sanitized: handle/ID only, no track data),
  // and whether the performance has started. Null when not in a Daily Double.
  ddPerformer: DailyDoublePerformerData | null;
  // The 5 song choices — populated ONLY on the contestant's own device, and
  // ONLY after accepting. Held here in memory only; never persisted to
  // localStorage/sessionStorage (see the ClientMsgType "dailyDouble.offer" doc
  // comment in protocol.ts). Cleared the instant the phase changes.
  ddOffer: DailyDoubleSongChoice[] | null;
  // The final average rating + points once a Daily Double resolves. Transient
  // — cleared on the next state change.
  ddResult: DailyDoubleResultData | null;
  // Sanitized "who's picking the next cell" status (handle + hasPicked), or
  // null when nobody is the designated picker (admin picks instead — the
  // original, unchanged behavior). Legal during BOARD (right after a Daily
  // Double) and KARAOKE (the normal post-round AND-gate with the skip vote).
  pickerStatus: PickerStatusData | null;
  // The board (category/points/exhausted — no track metadata, §4A-safe),
  // populated ONLY on the designated picker's own device so they can render
  // a tappable grid. Null for everyone else, and cleared once this device's
  // pick registers or the phase changes.
  pickableBoard: BoardData | null;
  // s8uim-02: true for a brief window right after `state` transitions into
  // KARAOKE or BOARD, until the trailing pickerStatus/pickableBoard
  // broadcasts land (or a short fallback timer expires, whichever first).
  // The server sends `state` before those trailing messages (several other
  // broadcasts are interleaved in between — see verdict s8uim-02), so
  // without this flag the picker's own device would, for that instant,
  // compute pickerStatus as still null and briefly render the wrong
  // (non-picker) screen. App.tsx renders a neutral loading state instead
  // while this is true.
  pickerSettling: boolean;
  // Solo Karaoke mode-lock flag (§4A): the ONLY Solo Karaoke signal mobile
  // ever receives is this bare boolean — no track title/artist/uri/lyrics.
  // When true, App.tsx short-circuits to the idle screen before the
  // GameState switch runs.
  soloKaraoke: boolean;
}

const HEARTBEAT_MS = 2000;
// Auto-reconnect backoff: doubles from base up to the cap (§3.2 resume).
const RECONNECT_BASE_MS = 500;
const RECONNECT_MAX_MS = 8000;
// s8uim-02: grace window for the `state` -> pickerStatus/pickableBoard gap.
// Deliberately short — real-world gap is a handful of broadcasts over the
// same already-open reliable stream, likely single-digit-to-low-double-digit
// ms, not hundreds. Kept short because this window also fires on every
// ordinary BOARD entry where NO picker status is ever coming (the common
// admin-picks path) — too long a window would trade a rare cosmetic flash
// for a routine, visible loading blip on every round instead.
const PICKER_SETTLE_MS = 150;

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
  kicked: false,
  rttMs: null,
  myPlayerID: null,
  ddPerformer: null,
  ddOffer: null,
  ddResult: null,
  pickerStatus: null,
  pickableBoard: null,
  pickerSettling: false,
  soloKaraoke: false,
};

export function useGame() {
  const [view, setView] = useState<GameView>(INITIAL);
  const clientRef = useRef<GameClient | null>(null);
  const heartbeatRef = useRef<ReturnType<typeof setInterval> | null>(null);
  // Maps the clientTime we stamped to compute RTT when the echo returns.
  const pendingPing = useRef<number | null>(null);
  // True between sending a buzz and receiving the buzzResult response.
  const awaitingBuzzResult = useRef(false);
  // s8uim-02: fallback timer that clears pickerSettling if the trailing
  // pickerStatus broadcast never arrives (e.g. a normal admin-picks BOARD
  // entry, where no such broadcast is ever sent at all).
  const pickerSettleTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Reconnect bookkeeping (§3.2 resume). We keep the handle + deviceFP stable
  // across drops so the server re-attaches this device to its saved score.
  const unmountedRef = useRef(false);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reconnectAttemptRef = useRef(0);
  const handleRef = useRef<string>("");
  // True while an establish() attempt is in flight (s5-ui-01). Set/cleared only
  // in establish(), via try/finally, never manually.
  const establishingRef = useRef(false);
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

  // Runs when a "kicked" error arrives (admin Telemetry panel's Kick
  // button). onAdminKick (server/internal/game/engine.go) sends this error
  // frame to the kicked connection but never closes the transport itself —
  // the connection is left open and the player's actions are just silently
  // ignored server-side from then on (p.Active/p.Banned checks). That means
  // there is no transport-drop race to worry about: this error frame is the
  // ONLY signal the client will ever get, so it must be impossible to miss.
  // Unlike handleDisconnect (which assumes an unintentional drop and
  // schedules a reconnect), a kick is deliberate: we proactively tear our
  // own end down — so no pointless heartbeats and, critically, no
  // auto-reconnect backoff ever fires and silently re-joins/obscures this —
  // and latch `kicked` so App.tsx renders an explicit, unmissable screen
  // instead of whatever the normal disconnect path would otherwise show.
  const handleKicked = useCallback(
    (message: string) => {
      stopHeartbeat();
      pendingPing.current = null;
      awaitingBuzzResult.current = false;
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = null;
      }
      reconnectAttemptRef.current = 0;
      const prev = clientRef.current;
      clientRef.current = null;
      // close() sets GameClient.closed, which suppresses that client's own
      // handleClose -> onState(false) -> handleDisconnect/scheduleReconnect
      // if it fires later for any reason — we've already done the
      // equivalent cleanup here, deliberately WITHOUT scheduling a
      // reconnect, regardless of the order these two things happen in.
      void prev?.close();
      patch({ kicked: true, error: message, conn: "disconnected" });
    },
    [patch, stopHeartbeat],
  );

  const wireHandlers = useCallback(
    (c: GameClient) => {
      c.on("welcome", (env: ServerEnvelope) => {
        const d = env.d as WelcomeData | undefined;
        if (d) patch({ joined: true, error: null, myPlayerID: d.playerID });
      });

      c.on("state", (env: ServerEnvelope) => {
        const d = env.d as StateData | undefined;
        if (!d) return;
        // s11fe-01: a visibility-triggered resync re-delivers the CURRENT state
        // verbatim, with no real transition. The ROUND_ACTIVE branch below used
        // to infer "fresh round" from "previous state wasn't ADJUDICATE" — true
        // for a genuine BOARD/TRANSITION->ROUND_ACTIVE transition, but ALSO true
        // for a same-value ROUND_ACTIVE->ROUND_ACTIVE resync repeat, which wiped
        // judgedThisRound/buzzedAndLost for a player already locked out this
        // round just for backgrounding and refocusing their tab. Captured here
        // so the repeat can be told apart from a real transition below.
        let wasAlreadySameState = false;
        setView((v) => {
          wasAlreadySameState = v.state === d.state;
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
            } else if (v.state !== "ROUND_ACTIVE") {
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
            // else: v.state was already ROUND_ACTIVE (resync repeat, no real
            // transition) — leave judgedThisRound/buzzedAndLost/lockedBy/
            // lastVerdict exactly as they are.
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
          // ddResult is intentionally NOT cleared here: the server broadcasts
          // dailyDouble.result just before the state flips to BOARD, so
          // clearing it on "state !== DAILY_DOUBLE" would erase it before it's
          // ever rendered. It persists until the next Daily Double starts or a
          // fresh round begins (below).
          if (d.state !== "DAILY_DOUBLE") {
            next.ddPerformer = null;
            next.ddOffer = null;
          }
          if (d.state === "ROUND_ACTIVE" && v.state !== "ADJUDICATE") {
            next.ddResult = null;
          }
          // Picker status/board are only ever meaningful during BOARD (right
          // after a Daily Double) or KARAOKE (the normal post-round AND-gate)
          // — clear both on leaving either, same pattern as ddPerformer/ddOffer.
          if (d.state !== "BOARD" && d.state !== "KARAOKE") {
            next.pickerStatus = null;
            next.pickableBoard = null;
          }
          // s8uim-02: entering BOARD/KARAOKE opens the settling window;
          // leaving either closes it immediately (nothing to bridge).
          // s11fe-02: a resync repeat of the SAME BOARD/KARAOKE phase must not
          // re-open it — that re-triggers the settling flash (and the timer
          // rearm below) on every tab refocus with nothing having changed.
          if (d.state === "BOARD" || d.state === "KARAOKE") {
            if (!wasAlreadySameState) next.pickerSettling = true;
          } else {
            next.pickerSettling = false;
          }
          return { ...v, ...next };
        });
        // Always replace any pending settle timer with a fresh decision for
        // the NEW state — a rapid state-to-state hop must not let a stale
        // timer from the previous phase clear pickerSettling late. Skipped on
        // a resync repeat of the same phase (s11fe-02) — nothing to bridge.
        if (pickerSettleTimerRef.current) {
          clearTimeout(pickerSettleTimerRef.current);
          pickerSettleTimerRef.current = null;
        }
        if ((d.state === "BOARD" || d.state === "KARAOKE") && !wasAlreadySameState) {
          pickerSettleTimerRef.current = setTimeout(() => {
            pickerSettleTimerRef.current = null;
            patch({ pickerSettling: false });
          }, PICKER_SETTLE_MS);
        }
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

      c.on("dailyDouble.performer", (env: ServerEnvelope) => {
        const d = env.d as DailyDoublePerformerData | undefined;
        if (!d) return;
        setView((v) => {
          // dd-ui-ms-1: server's sendFullSync re-sends this message with
          // performing:false on EVERY resync for as long as e.ddOffer is
          // non-nil — which spans the whole accept+song-pick window, not
          // just the pre-decision moment. A routine WebTransport reconnect
          // during that window (e.g. a wifi blip) used to be indistinguishable
          // from "a brand new Daily Double just started," so this handler
          // wiped ddOffer/ddResult out from under a contestant who was
          // already mid-flow — soft-locking them on a screen with no legal
          // action. Only treat it as fresh when the performer's playerID
          // actually changed (or there was no prior performer): that is the
          // one signal a genuinely new Daily Double always carries, since
          // ddPerformer is nulled out by the "state" handler the moment we
          // leave DAILY_DOUBLE, so a later DD for the SAME player still shows
          // up as a playerID transition from null.
          const isNewOffer = !v.ddPerformer || v.ddPerformer.playerID !== d.playerID;
          return {
            ...v,
            ddPerformer: d,
            ...(isNewOffer && !d.performing ? { ddResult: null, ddOffer: null } : {}),
          };
        });
      });

      // CONTESTANT'S OWN CONNECTION ONLY — see the doc comment on
      // ClientMsgType "dailyDouble.offer" in protocol.ts. Held only in this
      // in-memory view; never written to storage.
      c.on("dailyDouble.offer", (env: ServerEnvelope) => {
        const d = env.d as { songs: DailyDoubleSongChoice[] } | undefined;
        if (d) patch({ ddOffer: d.songs });
      });

      c.on("dailyDouble.result", (env: ServerEnvelope) => {
        const d = env.d as DailyDoubleResultData | undefined;
        if (d) patch({ ddResult: d });
      });

      // Sanitized "who's picking" status (handle + hasPicked) — safe for
      // every role (§4A). Lets non-pickers show "waiting on {handle}" and
      // the picker's own device know when its pick has registered.
      c.on("pickerStatus", (env: ServerEnvelope) => {
        const d = env.d as PickerStatusData | undefined;
        if (!d) return;
        // s8uim-02: the trailing message has landed — close the settling
        // window right away rather than waiting out PICKER_SETTLE_MS.
        if (pickerSettleTimerRef.current) {
          clearTimeout(pickerSettleTimerRef.current);
          pickerSettleTimerRef.current = null;
        }
        patch({ pickerStatus: d.pickerHandle ? d : null, pickerSettling: false });
      });

      // THIS DEVICE'S OWN CONNECTION ONLY — see the "pickableBoard" doc
      // comment in protocol.ts. Category/points/exhausted, no track data.
      c.on("pickableBoard", (env: ServerEnvelope) => {
        const d = env.d as BoardData | undefined;
        if (d) patch({ pickableBoard: d });
      });

      // Solo Karaoke mode-lock flag (§4A) — a bare boolean, zero track
      // metadata. This is the only Solo Karaoke broadcast mobile listens for.
      c.on("soloKaraokeActive", (env: ServerEnvelope) => {
        const d = env.d as SoloKaraokeActiveData | undefined;
        if (d) patch({ soloKaraoke: d.active });
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
        if (d?.code === "kicked") {
          handleKicked(d.message || "You've been removed from the game by the admin.");
          return;
        }
        patch({ error: d?.message ?? "Server error" });
      });
    },
    [patch, handleKicked],
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
      // Overlap guard (s5-ui-01): the backoff timer's attempt and a manual
      // retry can both pass a clientRef-only check while the first is parked in
      // fetchCertHashes(). Released in the finally below on EVERY exit path —
      // a stranded latch would reject every future reconnect forever. Dropping
      // an overlapping attempt is safe: the in-flight one either connects or
      // re-arms the backoff from its own catch.
      if (clientRef.current || establishingRef.current) return;
      establishingRef.current = true;
      try {
        handleRef.current = handle;
        patch({ conn: "connecting", error: null });
        saveHandle(handle);

        // The instance THIS invocation owns. Every ref clear below is gated on
        // identity so an attempt can never close/disown another's client
        // (s5-ui-01). Left undefined if fetchCertHashes() throws first.
        let client: GameClient | undefined;
        try {
          const serverCertHashes = await fetchCertHashes();
          client = new GameClient({
            url: WT_URL,
            // DEV-ONLY WebSocket fallback for phones on a plain-HTTP LAN origin,
            // where WebTransport is unavailable (non-secure context). Gated on
            // import.meta.env.DEV so a production bundle has no cleartext ws://
            // path at all — the server mounts /ws only with YFI_DEV_WS=1 AND
            // YFI_INSECURE_TRANSPORT=1 (no YFI_ENV backstop; prod may opt in and
            // only warns). In prod the fix is TLS + WebTransport, not this.
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
          // anything after ready throws, the QUIC session is OPEN and this was
          // its only reference — every backoff retry would leak another live
          // session and burn a per-IP limiter slot. Close OUR client, and
          // surrender the ref only while it is still ours (s5-ui-01); the
          // optional call covers fetchCertHashes throwing before construction,
          // where there is nothing to close.
          if (clientRef.current === client) clientRef.current = null;
          void client?.close();
          // Keep trying — a mid-game drop must not permanently wedge the player.
          scheduleReconnect();
          return;
        }

        // Unreachable: the block above either assigns `client` or returns from
        // its catch. Present so the send path below is type-narrowed.
        if (!client) return;

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
          if (clientRef.current === client) clientRef.current = null;
          void client.close();
          scheduleReconnect();
          return;
        }
      } finally {
        // Sole release point. Every return/throw above passes through here, so
        // the latch can never strand and lock the player out of reconnecting.
        establishingRef.current = false;
      }
    },
    [handleDisconnect, patch, scheduleReconnect, startHeartbeat, wireHandlers],
  );

  useEffect(() => {
    establishRef.current = (handle, isReconnect) =>
      void establish(handle, isReconnect);
  }, [establish]);

  const connect = useCallback(
    (handle: string) => {
      // A manual (re)join always supersedes a prior kick (kick is a soft
      // removal, not a ban — see the "kicked" vs "banned" error codes in
      // onAdminKick/onHello) — clear the latch so the kicked screen doesn't
      // linger over the fresh connect attempt.
      patch({ kicked: false });
      void establish(handle, false);
    },
    [establish, patch],
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

  const ddDecide = useCallback((accept: boolean) => {
    const c = clientRef.current;
    if (!c) return;
    void c.send<DailyDoubleDecisionData>({ t: "dailyDouble.decision", d: { accept } });
  }, []);

  const ddChoose = useCallback((trackID: string) => {
    const c = clientRef.current;
    if (!c) return;
    void c.send<DailyDoubleChooseData>({ t: "dailyDouble.choose", d: { trackID } });
  }, []);

  // Player-driven cell picking: only honored server-side if this device is
  // the designated picker (e.cellPicker) — see onPlayerSelectCell.
  //
  // s8uim-01: unlike the other fire-and-forget sends above, a dropped write
  // here is uniquely bad — it silently strands PickCellScreen's local
  // pickedRef lock with nothing to recover it except its own blind timeout
  // (no retry, no visible error). A `.catch` can't retry a send on a dead
  // stream either, but surfacing the failure immediately (rather than
  // waiting out PickCellScreen's full recovery window) at least gives the
  // player an on-screen signal that something went wrong instead of a
  // silent hang.
  const selectCell = useCallback(
    (row: number, col: number) => {
      const c = clientRef.current;
      if (!c) return;
      c.send<PlayerSelectCellData>({ t: "player.selectCell", d: { row, col } }).catch((e) => {
        patch({ error: e instanceof Error ? e.message : "Pick failed to send" });
      });
    },
    [patch],
  );

  useEffect(() => {
    return () => {
      unmountedRef.current = true;
      if (reconnectTimerRef.current) clearTimeout(reconnectTimerRef.current);
      if (heartbeatRef.current) clearInterval(heartbeatRef.current);
      if (pickerSettleTimerRef.current) clearTimeout(pickerSettleTimerRef.current);
      void clientRef.current?.close();
    };
  }, []);

  // Returning to a backgrounded tab never otherwise triggers a resync: a
  // live connection can sit through a one-shot broadcast (e.g. pickerStatus)
  // while hidden/throttled and never notice it missed anything, and a
  // stalled reconnect backoff timer is subject to the same background-tab
  // throttling. Force a resync (if connected) or an immediate reconnect
  // attempt (if a backoff is pending) the moment the tab becomes visible.
  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState !== "visible") return;
      const c = clientRef.current;
      if (c) {
        void c.send({ t: "resync" }).catch(() => {});
      } else if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = null;
        establishRef.current(handleRef.current, true);
      }
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, []);

  return { view, connect, buzz, vote, rate, ddDecide, ddChoose, selectCell };
}

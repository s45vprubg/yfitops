import { useCallback, useEffect, useRef, useState } from "react";
import { GameClient } from "@shared/client";
import type {
  AdminAwardData,
  AdminGradeData,
  AdminKickData,
  AdminPlaybackData,
  AdminSelectData,
  AdminSetThreshData,
  AdminViewData,
  BoardData,
  ClientMsgType,
  ErrorData,
  GameState,
  ScoreboardData,
  ServerEnvelope,
  TelemetryData,
  WelcomeData,
} from "@shared/protocol";
import { fetchCertHash, WT_URL } from "./config";

export type ConnStatus = "idle" | "connecting" | "authed" | "error" | "closed";

// The admin client holds NO authoritative state (§9). Everything below is a
// straight render of the most recent server payload. On reconnect the backend
// re-emits these via FULL_STATE_SYNC and the UI snaps back to the live state.
export interface AdminState {
  status: ConnStatus;
  error?: string;
  connected: boolean;
  playerID?: string;
  gameState?: GameState;
  board?: BoardData;
  adminView?: AdminViewData;
  telemetry?: TelemetryData;
  scoreboard?: ScoreboardData;
  nonce: number;
}

export interface AdminActions {
  login: (secret: string) => Promise<void>;
  logout: () => void;
  select: (d: AdminSelectData) => void;
  grade: (d: AdminGradeData) => void;
  playback: (action: AdminPlaybackData["action"]) => void;
  award: (d: AdminAwardData) => void;
  kick: (d: AdminKickData) => void;
  reveal: () => void;
  endRound: () => void;
  setThresh: (percent: number) => void;
  endGame: () => void;
}

const initial: AdminState = {
  status: "idle",
  connected: false,
  nonce: 0,
};

export function useAdmin(): [AdminState, AdminActions] {
  const [state, setState] = useState<AdminState>(initial);
  const clientRef = useRef<GameClient | null>(null);
  const patch = useCallback((p: Partial<AdminState>) => {
    setState((s) => ({ ...s, ...p }));
  }, []);

  // Tear down any live client on unmount.
  useEffect(() => {
    return () => {
      clientRef.current?.close().catch(() => {});
      clientRef.current = null;
    };
  }, []);

  const wire = useCallback(
    (client: GameClient) => {
      client.on("welcome", (env: ServerEnvelope) => {
        const d = env.d as WelcomeData;
        patch({ status: "authed", playerID: d.playerID, error: undefined });
      });
      client.on("error", (env: ServerEnvelope) => {
        const d = env.d as ErrorData;
        // A forbidden error during auth means the secret was rejected.
        patch({
          status: "error",
          error: `${d.code}: ${d.message}`,
        });
      });
      client.on("state", (env: ServerEnvelope) => {
        patch({ gameState: (env.d as { state: GameState }).state });
      });
      client.on("board", (env: ServerEnvelope) => {
        patch({ board: env.d as BoardData });
      });
      client.on("adminView", (env: ServerEnvelope) => {
        patch({ adminView: env.d as AdminViewData });
      });
      client.on("telemetry", (env: ServerEnvelope) => {
        patch({ telemetry: env.d as TelemetryData });
      });
      client.on("scoreboard", (env: ServerEnvelope) => {
        patch({ scoreboard: env.d as ScoreboardData });
      });
      // fullSync is a bundle; the backend follows it with the individual
      // payloads above, but accept a combined shape too for robustness.
      client.on("fullSync", (env: ServerEnvelope) => {
        const d = (env.d ?? {}) as Partial<{
          state: GameState;
          board: BoardData;
          adminView: AdminViewData;
          telemetry: TelemetryData;
          scoreboard: ScoreboardData;
        }>;
        patch({
          ...(d.state ? { gameState: d.state } : {}),
          ...(d.board ? { board: d.board } : {}),
          ...(d.adminView ? { adminView: d.adminView } : {}),
          ...(d.telemetry ? { telemetry: d.telemetry } : {}),
          ...(d.scoreboard ? { scoreboard: d.scoreboard } : {}),
        });
      });
      // Keep the nonce mirror fresh for the status display.
      client.onAny(() => patch({ nonce: client.nonce }));
    },
    [patch],
  );

  const login = useCallback(
    async (secret: string) => {
      // Replace any prior connection.
      await clientRef.current?.close().catch(() => {});
      patch({ status: "connecting", error: undefined });

      const certHashes = await fetchCertHash();
      const client = new GameClient({
        url: WT_URL,
        serverCertHashes: certHashes,
        onState: (connected) => {
          patch({ connected });
          setState((s) =>
            connected || s.status === "error"
              ? s
              : { ...s, status: "closed" },
          );
        },
      });
      clientRef.current = client;
      wire(client);

      try {
        await client.connect();
      } catch (e) {
        patch({
          status: "error",
          error: `connect failed: ${e instanceof Error ? e.message : String(e)}`,
        });
        return;
      }

      // Authenticate. The server replies with welcome (ok) or error/forbidden.
      await client.send({
        t: "hello",
        d: { role: "admin", adminSecret: secret },
      });
    },
    [patch, wire],
  );

  const logout = useCallback(() => {
    clientRef.current?.close().catch(() => {});
    clientRef.current = null;
    setState(initial);
  }, []);

  // Generic typed sender. GameClient stamps the latest nonce automatically.
  const sendAction = useCallback(<D>(t: ClientMsgType, d?: D) => {
    const c = clientRef.current;
    if (!c) return;
    c.send({ t, d }).catch(() => {});
  }, []);

  const actions: AdminActions = {
    login,
    logout,
    select: (d) => sendAction<AdminSelectData>("admin.select", d),
    grade: (d) => sendAction<AdminGradeData>("admin.grade", d),
    playback: (action) =>
      sendAction<AdminPlaybackData>("admin.playback", { action }),
    award: (d) => sendAction<AdminAwardData>("admin.award", d),
    kick: (d) => sendAction<AdminKickData>("admin.kick", d),
    reveal: () => sendAction("admin.reveal"),
    endRound: () => sendAction("admin.endRound"),
    setThresh: (percent) =>
      sendAction<AdminSetThreshData>("admin.setThresh", { percent }),
    endGame: () => sendAction("admin.endGame"),
  };

  return [state, actions];
}

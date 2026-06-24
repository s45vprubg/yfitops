# yfitops V2 — Admin Control Room

A high-information-density, dark control-room dashboard for the host
(design_doc §8C, §9). React 19 + Vite + TypeScript + Tailwind. Connects to the
Go backend over WebTransport via the shared `GameClient` with role `admin`.

This is a **trusted, token-gated client** (§9). It holds **no authoritative
state** — every panel renders straight from the latest server payload. On
reconnect, re-authenticating triggers a `FULL_STATE_SYNC` from the backend that
restores the UI.

## Layout

- **Top bar** — game state, best-effort Volume (UI-only; no protocol channel),
  Skip Voting Threshold slider (50–100%, sends `admin.setThresh`), Pause/Resume
  (`admin.playback`), End Game (`admin.endGame`), connection status + nonce.
- **Left (Queuing)** — interactive board miniature from the `board` payload.
  Clicking a live cell sends `admin.select{row,col}`. Exhausted cells disabled.
- **Center (Evaluation)** — highlights the buzzing user and shows the correct
  artist/song (admin-only reveal). Three large grading buttons:
  **Correct / Partial (artist|song) / Incorrect** → `admin.grade`. Manual
  overrides: Force End Round (`admin.endRound`), Reveal (`admin.reveal`),
  Award Points with player picker + delta (`admin.award`).
- **Right (Telemetry)** — live connection log from `telemetry`: handle, RTT,
  score, active flag, with Kick / Ban (`admin.kick{playerID,ban}`).

Every action echoes the current nonce automatically (`GameClient` stamps it)
and is authorized implicitly by the authenticated connection.

## Run

```bash
npm install
npm run dev        # dev server (default http://localhost:5173)
npm run build      # tsc -b && vite build  → dist/
npm run preview    # serve the production build
npm run typecheck  # tsc --noEmit
```

## Environment variables

| Var             | Default                          | Purpose                                              |
| --------------- | -------------------------------- | ---------------------------------------------------- |
| `VITE_WT_URL`   | `https://<host>:4433/wt`         | WebTransport endpoint                                |
| `VITE_HTTP_URL` | `http://<host>:8777`             | Plain-HTTP base for the dev `/cert-hash` endpoint    |

`<host>` defaults to the hostname the dashboard is served from. For dev with a
self-signed cert, the app fetches the SHA-256 hash from `${VITE_HTTP_URL}/cert-hash`
and passes it as `serverCertificateHashes` so the browser accepts the
connection without a CA. In production behind a real CA the hash is optional.

Example:

```bash
VITE_WT_URL=https://10.0.0.5:4433/wt VITE_HTTP_URL=http://10.0.0.5:8777 npm run dev
```

## Auth

The login screen takes the `ADMIN_SECRET` password. On submit the app connects
and sends `Hello{role:"admin", adminSecret}`. A server `error{code:"forbidden"}`
surfaces as a login error. **The secret must match the backend's `ADMIN_SECRET`.**

## Shared contracts

Imports `../shared/protocol.ts` and `../shared/client.ts` via the `@shared`
alias (configured in `vite.config.ts` and `tsconfig.app.json`). These are FIXED
contracts — do not modify them here.

> WebTransport requires a Chromium-based browser (Chrome/Edge).

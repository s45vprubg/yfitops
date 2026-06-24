# yfitops V2 — Mobile Buzzer PWA

The zero-install attendee buzzer for *Name That Spotify V2*. A React 19 + Vite +
TypeScript + Tailwind PWA. It connects to the Go backend over WebTransport via
the shared `GameClient` and is driven **purely off the server's `GameState`
flags + sanitized payloads** (`lockout`, `buzzResult`, `voteState`).

## The contract this client honors (§4A)

This client is deliberately blind. It NEVER receives or displays track title,
artist, Spotify URI, album art, or lyrics. The only things it knows are control
flags and the small sanitized payloads. If you ever find yourself reaching for
`reveal`/`lyrics` data in here, you've broken the contract — those go to the
stage/admin only.

## Screens (design_doc §8B)

| GameState                                   | Screen                                   |
| ------------------------------------------- | ---------------------------------------- |
| (not joined)                                | Login/Join — handle input + Join Game    |
| `LOBBY` `BOARD` `TRANSITION` `ADJUDICATE` `GAME_OVER` | Idle — status text                |
| `ROUND_ACTIVE`                              | Giant full-screen GUESS button           |
| `LOCKED_OUT` / after `buzzResult{won:false}`| Locked overlay (who's guessing / wrong)  |
| `KARAOKE`                                   | "Vote for Next Track" + vote progress    |
| `DAILY_DOUBLE`                              | 5-star confidence rating                 |

## Run

```bash
npm install
npm run dev      # dev server (Vite)
npm run build    # type-check + production build into dist/
npm run preview  # serve the built bundle
npm run typecheck
```

> Note: `npm run dev` serves over plain HTTP. WebTransport requires a secure
> context, but `http://localhost` counts as secure, so dev works on the host
> machine. To test from a phone on the LAN you must serve the PWA over HTTPS
> (e.g. `vite preview` behind a TLS proxy, or a tunneling tool).

## Environment variables

| Var             | Default                          | Meaning                                  |
| --------------- | -------------------------------- | ---------------------------------------- |
| `VITE_WT_URL`   | `https://<page-host>:4433/wt`    | WebTransport endpoint on the backend     |
| `VITE_HTTP_URL` | `http://<page-host>:8777`        | Plain-HTTP endpoint serving `/cert-hash` |

When the vars are unset, both default to the host the page was served from, so
a phone loading the PWA from the laptop's LAN IP automatically points its
WebTransport connection at that same IP. Set them explicitly for split deploys:

```bash
VITE_WT_URL=https://game.lan:4433/wt VITE_HTTP_URL=http://game.lan:8777 npm run build
```

## Dev self-signed cert (`serverCertificateHashes`)

For local dev the backend uses a short-lived self-signed cert. On connect, the
client fetches the base64 SHA-256 hash from `GET <VITE_HTTP_URL>/cert-hash`,
decodes it to bytes, and passes it to `GameClient` as `serverCertificateHashes`
so the browser accepts the cert without a CA. In production (real CA cert) that
endpoint is simply absent and the client connects normally. See
`src/lib/cert.ts`.

## Device fingerprint / resume (§3.2)

A stable random UUID is generated once and persisted in `localStorage`
(`yfitops.deviceFP`), sent in `Hello {role:"mobile", handle, deviceFP}`. This is
a soft, privacy-preserving session anchor for drop-out/resume — not a real
browser fingerprint. The handle is persisted too and prefilled on return.

## Heartbeats

The client sends a `heartbeat {clientTime}` every ~2s for RTT / active-pool
tracking (§4C, §3.8). RTT is shown in the status bar. Per §4B, client
timestamps are used only for RTT — never for buzz ordering, which is the
server's arrival-clock authority.

## Project layout

```
src/
  App.tsx                 state-flag router
  main.tsx                React entry
  index.css               Tailwind + mobile resets (safe-area, no zoom)
  lib/
    env.ts                resolves VITE_WT_URL / VITE_HTTP_URL
    cert.ts               fetch + decode dev cert hash
    fingerprint.ts        localStorage deviceFP + handle
    useGame.ts            GameClient wiring, heartbeats, sanitized state
  components/StatusBar.tsx
  screens/
    JoinScreen.tsx
    IdleScreen.tsx
    BuzzScreen.tsx        giant GUESS + locked overlay
    VoteScreen.tsx
    DailyDoubleScreen.tsx
```

The shared protocol/client live in the sibling `../shared` dir and are imported
via the `@shared/*` alias (configured in `vite.config.ts` + `tsconfig.json`).

## Icons

`public/icons/icon-192.png` and `icon-512.png` are generated placeholders.
Replace with real branded artwork before shipping.

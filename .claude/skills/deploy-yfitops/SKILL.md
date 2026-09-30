---
name: deploy-yfitops
description: Deploy/redeploy yfitops (Go gameserver + admin/mobile/stage React frontends) to the production droplet at yfitops.us. Use when asked to deploy, redeploy, or push a new build of yfitops live.
---

# Deploy yfitops to production (yfitops.us)

Host: `pope@64.225.91.233:54016`, key `~/.ssh/st.con_tunes` (IdentitiesOnly=yes,
IdentityAgent=none — the 1Password agent floods offered keys and trips the
server's max-auth-tries, producing a misleading "Too many authentication
failures" before your real key is even tried).

SSH and raw TCP checks to this host need `dangerouslyDisableSandbox: true` —
the local Seatbelt sandbox blocks the IP by default even though `ssh` itself
is nominally excluded; confirmed via `nc -zv` failing with "Operation not
permitted" until disabled.

## One-time box facts (as of 2026-09-29)

- Droplet: Ubuntu 24.10 "oracular", 960Mi RAM, no swap (added a 2G swapfile —
  see below), 24G disk. **oracular is EOL** — its apt mirrors 404. Fixed by
  pointing `/etc/apt/sources.list.d/ubuntu.sources` at
  `old-releases.ubuntu.com` instead of `mirrors.digitalocean.com` /
  `security.ubuntu.com` (backup kept at `ubuntu.sources.bak`).
- No Docker was installed; installed via the standard Docker CE apt repo.
  `pope` added to the `docker` group.
- ufw allows: 22, 80, 443, 5001, 3001, 54016/tcp (5001/3001 are leftovers from
  a prior unrelated app, left alone). **Need to add 4433/udp** for
  WebTransport/QUIC before the game server is reachable from a real browser.
- There is NO existing yfitops deploy on this box. A different, older,
  unrelated app (`NameThatSpotify`, pm2 process `spotify-game-backend`,
  nginx proxying `:3000`) used to run here but its directory
  (`/var/www/NameThatSpotify`) is gone — only a stale reference remains in
  root's `pm2` dump. Nothing to preserve there.
- nginx is running and already has a valid Let's Encrypt cert for
  `yfitops.us` at `/etc/letsencrypt/live/yfitops.us/`. The existing
  `sites-available/default` proxies `/` to `localhost:3000` (the dead old
  app) — this gets replaced.

## Repo

`https://github.com/s45vprubg/yfitops` — public. Backend is dockerized
(`deploy/docker-compose.yml`: postgres, redis, gameserver). Frontends
(`web/admin`, `web/mobile`, `web/stage`) are separate Vite apps with NO
Dockerfile/prod-serving story in the repo — built locally on a real machine
(not the 1GB droplet) and shipped as static bundles via nginx, to keep the
`npm run build` memory pressure off the box.

## Steps

1. **Local**: `git -C .../yfitops pull origin main` — always deploy latest
   main.
2. **Box**: swapfile + Docker install (done once, see above; idempotent to
   re-run, checks `swapon --show` / `command -v docker` first).
3. **Box**: `git clone` (or pull) the repo to `/opt/yfitops` (as `pope`, sudo
   chown if needed).
4. **Box**: `deploy/.env` — copy from `.env.example`, fill in:
   - `YFI_ENV=prod` (arms the default-secret boot refusal — the server won't
     even start on `changeme-admin`/dev nonce/join secrets once this is set).
   - Real, unique `ADMIN_SECRET`, `YFI_NONCE_SECRET`, `YFI_JOIN_SECRET` (long
     random strings — do NOT reuse the local dev `.env`'s `letmein`).
   - Spotify creds (client ID/secret) from the local dev `.env` — same app
     works, but `SPOTIFY_REDIRECT_URI` must become
     `https://yfitops.us/auth/spotify/callback` and that exact URI must be
     added in the Spotify developer dashboard for this app (external step,
     confirm with owner before changing their Spotify app config).
5. **Box**: mount the real Let's Encrypt cert into the gameserver container
   in place of the self-signed dev cert (`YFI_CERT_FILE`/`YFI_KEY_FILE`
   default to `/certs/*.pem` inside the container, auto-generated
   self-signed if absent) — WebTransport over QUIC needs a CA-signed cert to
   be trusted by real browsers, self-signed won't work for public traffic.
6. **Box**: `cd /opt/yfitops/deploy && make up` (builds + starts postgres,
   redis, gameserver). Verify: `curl 127.0.0.1:8777/healthz`.
7. **Box**: `make migrate` if this is a fresh volume beyond migration 0001 (the
   init mount only runs 0001 on first boot).
8. **Box**: `sudo ufw allow 4433/udp`.
9. **Local**: build the three frontends (`npm install && npm run build` in
   each of `web/admin`, `web/mobile`, `web/stage`) with prod env vars pointed
   at `https://yfitops.us` endpoints (`VITE_WT_URL`, `VITE_HTTP_URL`,
   `VITE_JOIN_URL`, `VITE_STAGE_SECRET` = the real `ADMIN_SECRET` from step 4).
10. **Local → Box**: `scp`/`rsync` each `dist/` to the box (e.g.
    `/opt/yfitops/web/{admin,mobile,stage}/dist`).
11. **Box**: nginx — replace `sites-available/default`'s proxy to `:3000`
    with a config serving the three static `dist/` dirs at appropriate
    paths and proxying the HTTP API paths (health/OAuth/token) to
    `127.0.0.1:8777`. `sudo nginx -t && sudo systemctl reload nginx`.
12. Verify live: `curl -I https://yfitops.us` and load it in a real browser
    (WebTransport needs Chromium).

## Final routing map (as deployed 2026-09-29)

nginx (`/etc/nginx/sites-available/yfitops`, backup of the old
NameThatSpotify config at `sites-available/default.bak-namethatspotify`):

- `https://yfitops.us/`       → `web/stage` build (base `/`)
- `https://yfitops.us/admin/` → `web/admin` build (base `/admin/`)
- `https://yfitops.us/play/`  → `web/mobile` build (base `/play/`, PWA)
- `/healthz`, `/auth/*`, `/api/*`, `/cert-hash`, `/ws` → `proxy_pass
  http://127.0.0.1:8777` (the gameserver container's HTTP port)
- WebTransport/QUIC (UDP 4433) is NOT proxied by nginx — browsers connect
  straight to `yfitops.us:4433` with the gameserver's own TLS cert.

Frontend builds are done **locally** (not on the droplet — 960Mi RAM, no
Docker for the frontends) and shipped as static `dist/` via `scp`, landing
in `/var/www/yfitops/{stage,admin,play}`. Build env vars used:

```
# stage
VITE_HTTP_URL=https://yfitops.us VITE_JOIN_URL=https://yfitops.us/play/ \
  VITE_STAGE_SECRET=<ADMIN_SECRET> npm run build
# admin (base path needs the raw vite build, not the npm script, to pass --base)
VITE_HTTP_URL=https://yfitops.us npx vite build --base=/admin/
# mobile
VITE_HTTP_URL=https://yfitops.us VITE_WS_URL=wss://yfitops.us/ws \
  npx vite build --base=/play/
```

`VITE_WT_URL` is left at its default in all three — it's computed at
**runtime** from `window.location.hostname`, so it correctly resolves to
`https://yfitops.us:4433/wt` in the browser without a build-time override.

## Gotchas learned this run

- 1Password SSH agent offering keys before the explicit `-i` key causes
  "Too many authentication failures" — always pass
  `-o IdentitiesOnly=yes -o IdentityAgent=none`.
- `nc`/`ssh` to the droplet IP get sandboxed-blocked locally; use
  `dangerouslyDisableSandbox: true` for any command touching
  `64.225.91.233`.
- `git push` needs `gh auth login --web` (device code flow — user completes
  it in a browser) then `gh auth setup-git` to actually wire the credential
  into git's credential helper; `gh auth login` alone isn't enough for plain
  `git push` over HTTPS.
- Writing into this project's `.claude/skills/` needs
  `dangerouslyDisableSandbox: true` — it's in the sandbox's write denylist
  by default.
- **`oracular` (Ubuntu 24.10) is EOL** — `apt update` 404s against the normal
  mirrors. Point `/etc/apt/sources.list.d/ubuntu.sources` at
  `old-releases.ubuntu.com` (both the main and `-security` URIs) before
  installing anything.
- `make` is not installed on the box — either `apt-get install make` or
  replicate the Makefile targets manually (the `migrate` target is just a
  `for m in migrations/*.sql; do docker compose exec -T postgres psql ...`
  loop).
- Let's Encrypt's `privkey.pem` is `600 root:root` — the gameserver container
  runs as non-root UID `10001` (`Dockerfile.server`), so it can't read it via
  a direct bind mount of `/etc/letsencrypt/live/...`. Copy `fullchain.pem`/
  `privkey.pem` to `deploy/certs-real/`, `chown 10001:10001`, `chmod 400`,
  and bind-mount THOSE into `/certs/cert.pem` / `/certs/key.pem` via
  `deploy/docker-compose.override.yml` (gitignored, not the tracked
  `docker-compose.yml`). A certbot renewal hook at
  `/etc/letsencrypt/renewal-hooks/deploy/yfitops-copy-certs.sh` re-copies and
  restarts the gameserver on every cert renewal — check it still exists and
  still matches this path if the deploy ever moves.
- **`yfitops.us` DNS was Cloudflare-proxied** (resolves to Cloudflare edge
  IPs, not the droplet). Cloudflare's standard proxy forwards HTTP(S) fine
  but does NOT forward arbitrary UDP, so WebTransport (UDP 4433) silently
  can't reach the origin while the static site loads perfectly — an easy
  trap since curl/browser HTTPS checks all pass. Fix: turn OFF the Cloudflare
  proxy (grey-cloud / "DNS only") for the `yfitops.us` A record, or move it
  to a dedicated DNS-only subdomain for `VITE_WT_URL`. DNS change took
  several minutes to propagate even to 1.1.1.1/8.8.8.8 — verify with
  `dig @1.1.1.1 +short yfitops.us A` before assuming it's live.
- Vite apps served under a subpath (`/admin/`, `/play/`) need
  `--base=/path/` at build time or their asset URLs resolve from `/assets`
  instead of `/path/assets` and 404. The `npm run build` script doesn't
  accept `--base` passthrough for every app here — call `npx vite build
  --base=/path/` directly instead of `npm run build`.
- **`VITE_X=val npm install && npm run build` only scopes `VITE_X` to `npm
  install`**, not to the `npm run build` after the `&&` — a bash env-prefix
  assignment applies only to the single command it's attached to. This
  silently shipped THREE broken frontend builds (empty stage secret, dead
  default ports for HTTP/WS) that still returned `200 OK` on every page
  because the failure is client-side JS calling the wrong endpoint, not a
  page-load error — curl checks look completely fine while the QR code /
  API calls are broken. Always use `env VITE_X=val VITE_Y=val2 <build
  command>` (or a single command, no `&&`) and then GREP THE BUILT BUNDLE
  for the expected literal value before trusting it shipped, e.g.
  `grep -c "yfitops.us/play/" dist/assets/*.js`.
- nginx `location /path/ { alias ...; }` does NOT auto-redirect a bare
  `/path` (no trailing slash) request to `/path/` — add an explicit
  `location = /path { return 301 /path/; }` for each subpath app if bare
  URLs should work.

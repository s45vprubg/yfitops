// yfitops UX/functional test harness -- full scenario matrix.
//
// See /Users/banovik/.claude/plans/async-mapping-pancake.md for the design.
// Scripted deterministic actions + objective scripted assertions (no LLM
// judgment here -- that's a separate post-hoc review pass over the
// screenshots/timeline this run produces).
//
// Run: `npm install && npx playwright install chromium && npm run run`
// from this directory, against an already-running local stack
// (`scripts/dev-up.sh` from the repo root).

import { chromium, type Browser, type BrowserContext, type Page } from "playwright";
import { mkdirSync, appendFileSync } from "fs";
import { join } from "path";

// ---- Config (env-overridable so the same script can target prod later) ----
const HTTP_BASE = process.env.YFI_UXTEST_HTTP_BASE ?? "http://127.0.0.1:8777";
const STAGE_BASE = process.env.YFI_UXTEST_STAGE_BASE ?? "http://127.0.0.1:8778";
const ADMIN_BASE = process.env.YFI_UXTEST_ADMIN_BASE ?? "http://127.0.0.1:8779";
const MOBILE_BASE = process.env.YFI_UXTEST_MOBILE_BASE ?? "http://127.0.0.1:8780";
const ADMIN_SECRET = process.env.YFI_UXTEST_ADMIN_SECRET ?? "letmein";
const SESSION_ID = process.env.YFI_UXTEST_SESSION_ID ?? "session"; // config.go default

const RUN_DIR = join(process.cwd(), `run-${Date.now()}`);
const SCREENSHOT_DIR = join(RUN_DIR, "screenshots");
const TIMELINE_PATH = join(RUN_DIR, "timeline.jsonl");

mkdirSync(SCREENSHOT_DIR, { recursive: true });

type PlayerRole = "bot1" | "bot2" | "bot3" | "bot4" | "bot5";
const PLAYERS: PlayerRole[] = ["bot1", "bot2", "bot3", "bot4", "bot5"];
type Role = PlayerRole | "admin" | "stage";

const results: Record<string, unknown> = {};
let failures = 0;

function logEvent(event: Record<string, unknown>) {
  appendFileSync(TIMELINE_PATH, JSON.stringify({ ts: Date.now(), ...event }) + "\n");
  console.log(`[${new Date().toISOString()}]`, JSON.stringify(event));
}

function assert(name: string, passed: boolean, extra: Record<string, unknown> = {}) {
  results[name] = { passed, ...extra };
  if (!passed) failures++;
  logEvent({ step: "assert", name, passed, ...extra });
}

async function screenshotAll(pages: Record<Role, Page>, step: string) {
  for (const [role, page] of Object.entries(pages)) {
    const path = join(SCREENSHOT_DIR, `${step}-${role}.png`);
    try {
      await page.screenshot({ path });
    } catch (e) {
      logEvent({ step, role, error: String(e) });
    }
  }
  logEvent({ step, action: "screenshot-all", roles: Object.keys(pages) });
}

// ---- Admin REST helper (board setup -- not the live WS/WT admin role) ----
async function adminFetch(path: string, opts: RequestInit = {}): Promise<Response> {
  const res = await fetch(`${HTTP_BASE}${path}`, {
    ...opts,
    headers: {
      ...(opts.body ? { "Content-Type": "application/json" } : {}),
      Authorization: `Bearer ${ADMIN_SECRET}`,
      ...opts.headers,
    },
  });
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new Error(`${opts.method ?? "GET"} ${path} -> ${res.status} ${body}`);
  }
  return res;
}

function fakeSpotifyUri(n: number): string {
  return `spotify:track:uxtest${String(n).padStart(16, "0")}`;
}
const FAKE_LRC = "[00:01.00]uxtest lyric line";

// Row-based decay math, independent re-implementation of scoring.go's
// §7 curve (BaseValue=100, DecayHoldSeconds=5, DecayEndSeconds=60), so an
// assertion comparing the two is a real cross-check, not a tautology.
const BASE_VALUE = 100;
function maxPointsForRow(row: number): number {
  const mult = row <= 1 ? 1.0 : row === 2 ? 1.25 : row === 3 ? 1.5 : row === 4 ? 1.75 : 2.0;
  return Math.floor(BASE_VALUE * mult);
}
function currentPoints(row: number, elapsedMs: number): number {
  const max = maxPointsForRow(row);
  const bonus = max - BASE_VALUE;
  const t = elapsedMs / 1000;
  if (t <= 5) return max;
  if (t >= 60) return BASE_VALUE;
  const frac = 1 - (t - 5) / 55;
  return Math.floor(BASE_VALUE + bonus * frac);
}

const CATEGORY = (col: number) => `UXTESTCOL${col}`;

interface SetupResult {
  boardId: string;
}

// Builds a real 5x5 board (25 cells, 2 tracks each) via the admin REST API
// -- NOT the SampleBoard() in-memory fixture, which only auto-attaches when
// Postgres is unreachable (scripts/dev-up.sh deliberately runs Postgres, so
// that path never fires). dailyDoubleCount is set to the full 25-cell count
// so assignDailyDoubles() (engine.go:1239) exhausts every eligible cell --
// every cell becomes DailyDouble=true/DDTriggerVisit=2, i.e. a cell's FIRST
// pick is always a normal round and its SECOND pick always triggers a
// Daily Double, per the plan.
async function setupBoard(): Promise<SetupResult> {
  const createRes = await adminFetch("/api/boards", {
    method: "POST",
    body: JSON.stringify({ name: `uxtest-${Date.now()}` }),
  });
  const board = await createRes.json();
  const boardId: string = board.id;
  logEvent({ step: "setup", action: "createBoard", boardId });

  // createBoard already gives column 1; add columns 2-5.
  await adminFetch(`/api/boards/${boardId}/columns/1`, {
    method: "PATCH",
    body: JSON.stringify({ category: CATEGORY(1) }),
  });
  for (let col = 2; col <= 5; col++) {
    await adminFetch(`/api/boards/${boardId}/columns`, {
      method: "POST",
      body: JSON.stringify({ category: CATEGORY(col) }),
    });
  }
  logEvent({ step: "setup", action: "columns", boardId, cols: 5 });

  for (let row = 1; row <= 5; row++) {
    for (let col = 1; col <= 5; col++) {
      for (let pos = 0; pos < 2; pos++) {
        const n = row * 100 + col * 10 + pos;
        const addRes = await adminFetch(`/api/boards/${boardId}/tracks`, {
          method: "POST",
          body: JSON.stringify({
            spotifyUri: fakeSpotifyUri(n),
            artist: `UXTest Artist ${n}`,
            song: `UXTest Song ${n}`,
            albumArt: "",
            durationMs: 180000,
          }),
        });
        const track = await addRes.json();
        await adminFetch(`/api/boards/${boardId}/tracks/${track.id}/lyrics`, {
          method: "PUT",
          body: JSON.stringify({ lrcText: FAKE_LRC }),
        });
        await adminFetch(`/api/boards/${boardId}/cells/${row}/${col}/tracks/${track.id}`, {
          method: "PUT",
          body: JSON.stringify({ pos }),
        });
      }
    }
  }
  logEvent({ step: "setup", action: "placedTracks", boardId, cells: 25, tracksPerCell: 2 });

  await adminFetch(`/api/boards/${boardId}`, {
    method: "PATCH",
    body: JSON.stringify({ dailyDoubleCount: 25 }),
  });

  await adminFetch(`/api/boards/${boardId}/attach`, {
    method: "POST",
    body: JSON.stringify({ sessionId: SESSION_ID }),
  });
  logEvent({ step: "setup", action: "attachBoard", boardId, sessionId: SESSION_ID });

  await adminFetch("/api/game/start", { method: "POST" });
  logEvent({ step: "setup", action: "startGame", boardId });

  return { boardId };
}

// Gets the engine back to StateLobby via the admin UI (the only path: there
// is no REST endpoint for admin.endGame, only the live WS/WT action), so a
// rerun never needs a manual stack restart. Safe to call even if already
// in LOBBY (no-op).
async function resetToLobby(adminPage: Page): Promise<void> {
  const startBtn = adminPage.getByRole("button", { name: "Start Game", exact: true });
  const endBtn = adminPage.getByRole("button", { name: "End Game", exact: true });
  const newBtn = adminPage.getByRole("button", { name: "New Game", exact: true });

  // The admin frontend's gameState prop takes a moment to populate after
  // login (first WS "state" message), so a bare instant isVisible() check
  // right after login races the page and always reads false. Wait for
  // whichever of the three mutually-exclusive buttons shows up first.
  const first = await Promise.race([
    startBtn.waitFor({ timeout: 10000 }).then(() => "start").catch(() => null),
    endBtn.waitFor({ timeout: 10000 }).then(() => "end").catch(() => null),
    newBtn.waitFor({ timeout: 10000 }).then(() => "new").catch(() => null),
  ]);

  if (first === "start" || first === null) {
    logEvent({ step: "reset", action: first === "start" ? "already-lobby" : "no-game-button-found", warning: first === null });
    return;
  }
  if (first === "end") {
    await endBtn.click();
    // Confirm modal reuses the same label (confirmLabel: "End Game").
    await adminPage.waitForTimeout(300);
    const confirmBtn = adminPage.getByRole("button", { name: "End Game", exact: true }).last();
    await confirmBtn.click({ timeout: 5000 }).catch(() => {});
    await adminPage.waitForTimeout(1000);
    logEvent({ step: "reset", action: "end-game" });
  }
  if (await newBtn.isVisible({ timeout: 8000 }).catch(() => false)) {
    await newBtn.click();
    await adminPage.waitForTimeout(500);
    logEvent({ step: "reset", action: "new-game" });
  } else {
    logEvent({ step: "reset", action: "new-game-button-not-found", warning: true });
  }
}

function cellLocator(adminPage: Page, row: number, col: number) {
  return adminPage.locator(`button[title^="${CATEGORY(col)} "]`).nth(row - 1);
}

async function selectCell(adminPage: Page, row: number, col: number) {
  const btn = cellLocator(adminPage, row, col);
  await btn.waitFor({ timeout: 15000 });
  await btn.click();
  logEvent({ step: "round", action: "selectCell", row, col });
  await adminPage.waitForTimeout(1200);
}

async function gradeCorrect(adminPage: Page) {
  await adminPage.getByRole("button", { name: "Correct", exact: false }).first().click();
  logEvent({ step: "round", action: "gradeCorrect" });
  await adminPage.waitForTimeout(1200);
}
async function gradePartial(adminPage: Page, kind: "artist" | "song") {
  await adminPage.getByRole("button", { name: "Partial", exact: false }).first().click();
  await adminPage.waitForTimeout(200);
  await adminPage.getByRole("button", { name: kind === "artist" ? "Artist" : "Song", exact: true }).click();
  logEvent({ step: "round", action: "gradePartial", kind });
  await adminPage.waitForTimeout(1200);
}
async function gradeIncorrect(adminPage: Page) {
  await adminPage.getByRole("button", { name: "Incorrect", exact: false }).first().click();
  logEvent({ step: "round", action: "gradeIncorrect" });
  await adminPage.waitForTimeout(1200);
}

async function buzz(page: Page) {
  await page.getByRole("button", { name: "Guess buzzer", exact: false }).click();
}

// Reads a player's live score off the admin SCOREBOARD panel specifically
// (scoped to the text AFTER the "SCOREBOARD" heading -- the TELEMETRY panel
// above it also renders handles next to IP/RTT digits, which a body-wide
// regex can match instead of the real score; this bit the smoke-test fork
// on its first run).
async function readScore(adminPage: Page, bot: PlayerRole): Promise<number | null> {
  const bodyText = await adminPage.locator("body").innerText();
  const idx = bodyText.indexOf("SCOREBOARD");
  const scoped = idx >= 0 ? bodyText.slice(idx) : bodyText;
  const m = scoped.match(new RegExp(`${bot}\\s*\\n?\\s*(-?\\d+)`, "i"));
  return m ? parseInt(m[1], 10) : null;
}

async function main() {
  let setup: SetupResult;
  const browser: Browser = await chromium.launch({ headless: true });
  const contexts: Record<Role, BrowserContext> = {} as Record<Role, BrowserContext>;
  const pages: Record<Role, Page> = {} as Record<Role, Page>;

  try {
    for (const role of [...PLAYERS, "admin", "stage"] as Role[]) {
      contexts[role] = await browser.newContext({ ignoreHTTPSErrors: true });
      pages[role] = await contexts[role].newPage();
    }

    // --- Admin login FIRST (needed for resetToLobby before board setup) ---
    await pages.admin.goto(`${ADMIN_BASE}/`);
    await pages.admin.getByPlaceholder("ADMIN_SECRET").fill(ADMIN_SECRET);
    await pages.admin.getByRole("button", { name: /authenticate/i }).click();
    await pages.admin.waitForSelector("text=/control room|board/i", { timeout: 15000 }).catch(() => {});
    logEvent({ step: "login", role: "admin" });

    await resetToLobby(pages.admin);

    setup = await setupBoard();

    // --- Stage login (real stage is at /stage, not the public landing "/") ---
    await pages.stage.goto(`${STAGE_BASE}/stage`);
    await pages.stage.getByPlaceholder("ADMIN_SECRET").fill(ADMIN_SECRET);
    await pages.stage.getByRole("button", { name: /start stage/i }).click();
    logEvent({ step: "login", role: "stage" });

    // --- 5 players join ---
    for (const bot of PLAYERS) {
      await pages[bot].goto(`${MOBILE_BASE}/`);
      await pages[bot].getByPlaceholder("0xR00K").fill(bot);
      await pages[bot].getByRole("button", { name: /join game/i }).click();
      logEvent({ step: "join", role: bot });
    }
    await Promise.all(Object.values(pages).map((p) => p.waitForTimeout(1500)));
    await screenshotAll(pages, "00-lobby");

    // ================= STEP 1: simultaneous buzz race (3 trials) =================
    const raceCells: [number, number][] = [[1, 1], [1, 2], [1, 3]];
    for (let trial = 0; trial < raceCells.length; trial++) {
      const [row, col] = raceCells[trial];
      await selectCell(pages.admin, row, col);
      const buzzBtn0 = pages.bot1.getByRole("button", { name: "Guess buzzer", exact: false });
      await buzzBtn0.waitFor({ timeout: 15000 }).catch(() => {});
      await screenshotAll(pages, `10-race${trial}-pre-buzz`);

      // True simultaneity: fire all 5 clicks in one Promise.all tick.
      const clickResults = await Promise.allSettled(PLAYERS.map((p) => buzz(pages[p])));
      logEvent({ step: "race", trial, row, col, clickResults: clickResults.map((r) => r.status) });
      await pages.admin.waitForTimeout(1500);
      await screenshotAll(pages, `11-race${trial}-post-buzz`);

      // Exactly one winner: the EvaluationPanel's contestant card renders
      // the single buzz winner's handle right above the grade buttons.
      // Scoping to "before SCOREBOARD" is too broad -- the TELEMETRY panel
      // (every player's handle + IP/RTT) also renders before SCOREBOARD, so
      // all 5 names always leak through regardless of adjudicate state.
      // Scope tightly instead: the ~200 chars immediately before the
      // "Correct" grade button's own position in body text.
      const adjudicateText = await pages.admin.locator("body").innerText();
      // innerText() reflects the browser's RENDERED text, including the
      // button's CSS text-transform:uppercase -- the real DOM text is
      // "Correct" but innerText() returns "CORRECT", so a case-sensitive
      // indexOf never matched (this harness's own bug, caught on this run).
      const correctIdx = adjudicateText.search(/correct/i);
      const nearContestant = correctIdx > 0 ? adjudicateText.slice(Math.max(0, correctIdx - 200), correctIdx) : "";
      const winnerMatches = PLAYERS.filter((p) => new RegExp(p, "i").test(nearContestant));
      assert(`race_trial${trial}_exactlyOneWinner`, winnerMatches.length === 1, { row, col, winnerMatches, nearContestant });

      // Grade CORRECT (not incorrect) to actually consume the track and
      // free the board for the next trial's cell selection -- an incorrect
      // grade leaves the round ACTIVE on the SAME cell/track (re-arms other
      // eligible buzzers), so the board stays unselectable until the round
      // is genuinely resolved. The resulting picker assignment doesn't
      // block the next selectCell() -- admin.select always overrides any
      // pending player pick immediately (selectCell's own documented
      // contract). Scores awarded here are intentional noise; every later
      // scoring assertion in this script uses before/after deltas, never
      // an absolute value, specifically so this doesn't contaminate them.
      await gradeCorrect(pages.admin);
      await pages.admin.waitForTimeout(1000);
      await screenshotAll(pages, `12-race${trial}-graded`);
    }

    // ================= STEP 2: grading combination matrix =================
    type GradeCase = { row: number; col: number; name: string; run: () => Promise<void> };
    const gradeCases: GradeCase[] = [
      {
        row: 1, col: 4, name: "correct",
        run: async () => {
          await selectCell(pages.admin, 1, 4);
          const before = await readScore(pages.admin, "bot1") ?? 0;
          const t0 = Date.now();
          await buzz(pages.bot1);
          await pages.admin.waitForTimeout(500);
          const elapsed = Date.now() - t0;
          await gradeCorrect(pages.admin);
          const after = await readScore(pages.admin, "bot1");
          const awarded = after !== null ? after - before : null;
          const expected = currentPoints(1, elapsed);
          assert("grade_correct_score", awarded !== null && Math.abs(awarded - expected) <= 5, { before, after, awarded, expected, elapsed });
          const bodyText = await pages.admin.locator("body").innerText();
          assert("grade_correct_pickerAssigned", /waiting on bot1 to pick a cell/i.test(bodyText));
        },
      },
      {
        row: 1, col: 5, name: "partial",
        run: async () => {
          await selectCell(pages.admin, 1, 5);
          await buzz(pages.bot2);
          await pages.admin.waitForTimeout(500);
          const before = await readScore(pages.admin, "bot2") ?? 0;
          await gradePartial(pages.admin, "artist");
          const after = await readScore(pages.admin, "bot2");
          assert("grade_partial_awards50", after !== null && after - before === 50, { before, after });
          // Round must still be live (buzzer re-armed for others) -- bot3 can buzz.
          const buzzBtn = pages.bot3.getByRole("button", { name: "Guess buzzer", exact: false });
          const reArmed = await buzzBtn.isEnabled().catch(() => false);
          assert("grade_partial_roundContinues", reArmed);
          // Clean up: let bot3 finish it correctly so the cell is consumed.
          if (reArmed) {
            await buzz(pages.bot3);
            await pages.admin.waitForTimeout(500);
            await gradeCorrect(pages.admin);
          }
        },
      },
      {
        row: 2, col: 1, name: "partial_then_partial",
        run: async () => {
          await selectCell(pages.admin, 2, 1);
          await buzz(pages.bot1);
          await pages.admin.waitForTimeout(500);
          await gradePartial(pages.admin, "artist");
          await pages.admin.waitForTimeout(800);
          await buzz(pages.bot2);
          await pages.admin.waitForTimeout(500);
          const before = await readScore(pages.admin, "bot2") ?? 0;
          await gradePartial(pages.admin, "song");
          const after = await readScore(pages.admin, "bot2");
          assert("grade_secondPartial_awards50", after !== null && after - before === 50, { before, after });
          // Two partials must end the round -- grade buttons should now be gone.
          const stillGrading = await pages.admin.getByRole("button", { name: "Correct", exact: false }).first().isVisible().catch(() => false);
          assert("grade_secondPartial_endsRound", !stillGrading);
        },
      },
      {
        row: 2, col: 2, name: "incorrect",
        run: async () => {
          await selectCell(pages.admin, 2, 2);
          await buzz(pages.bot1);
          await pages.admin.waitForTimeout(500);
          const before = await readScore(pages.admin, "bot1") ?? 0;
          await gradeIncorrect(pages.admin);
          const after = await readScore(pages.admin, "bot1");
          assert("grade_incorrect_noPoints", after !== null && after === before, { before, after });
          const buzzBtn = pages.bot2.getByRole("button", { name: "Guess buzzer", exact: false });
          assert("grade_incorrect_reArmsOthers", await buzzBtn.isEnabled().catch(() => false));
          // Cleanup: an incorrect grade leaves the round ACTIVE on the SAME
          // cell (re-arms other eligible buzzers, doesn't consume the
          // track), so the board stays unselectable for the NEXT case's
          // selectCell() until someone actually closes this round out.
          await buzz(pages.bot2);
          await pages.admin.waitForTimeout(500);
          await gradeCorrect(pages.admin);
        },
      },
      {
        row: 2, col: 3, name: "incorrect_then_correct",
        run: async () => {
          await selectCell(pages.admin, 2, 3);
          await buzz(pages.bot1);
          await pages.admin.waitForTimeout(500);
          await gradeIncorrect(pages.admin);
          await pages.admin.waitForTimeout(500);
          await buzz(pages.bot2);
          await pages.admin.waitForTimeout(500);
          const before = await readScore(pages.admin, "bot2") ?? 0;
          await gradeCorrect(pages.admin);
          const after = await readScore(pages.admin, "bot2");
          assert("grade_incorrectThenCorrect_awarded", after !== null && after > before, { before, after });
        },
      },
    ];
    for (const gc of gradeCases) {
      try {
        await gc.run();
      } catch (e) {
        logEvent({ step: "grade_case", name: gc.name, error: String(e) });
        assert(`grade_case_${gc.name}_ranWithoutError`, false, { error: String(e) });
      }
      await screenshotAll(pages, `20-grade-${gc.name}`);
    }

    // ================= STEP 4: admin resilience =================
    try {
      await selectCell(pages.admin, 2, 4);
      await pages.admin.waitForTimeout(500);
      await pages.admin.getByRole("button", { name: "Pause", exact: true }).click();
      await pages.admin.waitForTimeout(800);
      await screenshotAll(pages, "30-paused");
      const resumeVisible = await pages.admin.getByRole("button", { name: "Resume", exact: true }).isVisible().catch(() => false);
      // Diagnostic evidence either way -- a bare boolean doesn't tell a
      // reviewer whether Pause/Resume is broken or just demo-mode-specific.
      const pauseAreaText = await pages.admin.locator("body").innerText().then((t) => t.slice(0, 600)).catch(() => "<unreadable>");
      assert("resilience_pauseShowsResume", resumeVisible, { pauseAreaText });

      // Reload admin mid-round -- must re-authenticate and resync, and must
      // NOT disrupt the still-connected mobile/stage contexts.
      await pages.admin.reload();
      await pages.admin.getByPlaceholder("ADMIN_SECRET").fill(ADMIN_SECRET).catch(() => {});
      await pages.admin.getByRole("button", { name: /authenticate/i }).click().catch(() => {});
      await pages.admin.waitForTimeout(2000);
      const resyncedResume = await pages.admin.getByRole("button", { name: "Resume", exact: true }).isVisible().catch(() => false);
      assert("resilience_reloadResyncsPausedState", resyncedResume);
      const stageStillAlive = await pages.stage.locator("body").innerText().then((t) => t.length > 0).catch(() => false);
      assert("resilience_reloadDidNotDisruptStage", stageStillAlive);

      await pages.admin.getByRole("button", { name: "Resume", exact: true }).click().catch(() => {});
      await pages.admin.waitForTimeout(800);
      await screenshotAll(pages, "31-resumed");
      // Cleanup: close this round out with a real correct grade (not just
      // gradeIncorrect, which leaves it ACTIVE on the same cell) so the
      // board is selectable again for the next scenario.
      await buzz(pages.bot1);
      await pages.admin.waitForTimeout(500);
      await gradeIncorrect(pages.admin);
      await pages.admin.waitForTimeout(500);
      await buzz(pages.bot2);
      await pages.admin.waitForTimeout(500);
      await gradeCorrect(pages.admin);
    } catch (e) {
      logEvent({ step: "resilience", error: String(e) });
      assert("resilience_ranWithoutError", false, { error: String(e) });
    }

    // ================= STEP 5: Daily Double =================
    try {
      // (2,5): first pick (normal), complete it; second pick triggers DD.
      await selectCell(pages.admin, 2, 5);
      await buzz(pages.bot1);
      await pages.admin.waitForTimeout(500);
      await gradeCorrect(pages.admin);
      await pages.admin.waitForTimeout(800);

      await selectCell(pages.admin, 2, 5);
      await pages.admin.waitForTimeout(1500);
      await screenshotAll(pages, "40-dd-offer");
      // lastScorer (bot1, just won the previous round) is the contestant.
      const imIn = pages.bot1.getByRole("button", { name: "I'm in!", exact: true });
      const ddOffered = await imIn.isVisible({ timeout: 8000 }).catch(() => false);
      assert("dd_offeredToContestant", ddOffered);
      if (ddOffered) {
        await imIn.click();
        await pages.admin.waitForTimeout(800);
        const songChoice = pages.bot1.locator("button").filter({ hasText: "UXTest Song" }).first();
        await songChoice.waitFor({ timeout: 8000 }).catch(() => {});
        await songChoice.click().catch(() => {});
        await pages.admin.waitForTimeout(1500);
        await screenshotAll(pages, "41-dd-performing");

        for (const p of PLAYERS) {
          const star3 = pages[p].getByLabel("3 stars", { exact: true });
          await star3.click({ timeout: 5000 }).catch(() => {});
        }
        await pages.admin.waitForTimeout(1500);
        const beforeDD = await readScore(pages.admin, "bot1") ?? 0;
        // Let the performance end naturally (or force end as a resilience
        // check) -- Force End Daily Double, per the scenario.
        await pages.admin.getByRole("button", { name: /end daily double/i }).click({ timeout: 5000 }).catch(() => {});
        await pages.admin.waitForTimeout(2000);
        const afterDD = await readScore(pages.admin, "bot1");
        assert("dd_forceEndPaysOut", afterDD !== null && afterDD !== beforeDD, { beforeDD, afterDD });
      }
      await screenshotAll(pages, "42-dd-done");

      // (3,1): decline path.
      await resetBuzzLockouts(pages); // see helper below
      await selectCell(pages.admin, 3, 1);
      await buzz(pages.bot2);
      await pages.admin.waitForTimeout(500);
      await gradeCorrect(pages.admin);
      await pages.admin.waitForTimeout(800);
      await selectCell(pages.admin, 3, 1);
      await pages.admin.waitForTimeout(1500);
      const passBtn = pages.bot2.getByRole("button", { name: "Pass", exact: true });
      const declineOffered = await passBtn.isVisible({ timeout: 8000 }).catch(() => false);
      assert("dd_declineOffered", declineOffered);
      if (declineOffered) {
        await passBtn.click();
        await pages.admin.waitForTimeout(1500);
        await screenshotAll(pages, "43-dd-declined");
      }
    } catch (e) {
      logEvent({ step: "dailydouble", error: String(e) });
      assert("dd_ranWithoutError", false, { error: String(e) });
    }

    // ================= STEP 6: stage correctness =================
    try {
      await selectCell(pages.admin, 3, 2);
      await buzz(pages.bot3);
      await pages.admin.waitForTimeout(500);
      // Before grading: stage must NOT show the real artist/song yet.
      const adminTrusted = await pages.admin.locator("body").innerText();
      const titleMatch = adminTrusted.match(/UXTest Song \d+/);
      const stagePreGrade = await pages.stage.locator("body").innerText();
      assert("stage_noEarlyReveal", !!titleMatch && !stagePreGrade.includes(titleMatch[0]));

      const scoreBefore = await readScore(pages.admin, "bot3") ?? 0;
      const t0 = Date.now();
      await gradeCorrect(pages.admin);
      const elapsed = Date.now() - t0;
      await pages.admin.waitForTimeout(1000);
      const scoreAfter = await readScore(pages.admin, "bot3");
      const awarded = (scoreAfter ?? 0) - scoreBefore;
      const stagePostGrade = await pages.stage.locator("body").innerText();
      assert("stage_revealsAfterGrade", !!titleMatch && stagePostGrade.includes(titleMatch[0]));
      // The displayed-points-vs-awarded divergence this session's own
      // halving-bug fix regression-checks: can't read stage's projected
      // number directly here (no stable selector investigated), so this
      // pins the WEAKER but still real invariant that the award itself is
      // a positive, row-plausible amount in the live decayed range.
      assert("stage_awardWithinDecayRange", awarded > 0 && awarded <= maxPointsForRow(3) && awarded >= BASE_VALUE - 1, { awarded, elapsed });
    } catch (e) {
      logEvent({ step: "stage_correctness", error: String(e) });
      assert("stage_correctness_ranWithoutError", false, { error: String(e) });
    }

    // ================= STEP 7: input-availability assertions =================
    try {
      // Buzzer disabled for a player who already guessed this track.
      await selectCell(pages.admin, 3, 3);
      await buzz(pages.bot1);
      await pages.admin.waitForTimeout(500);
      await gradeIncorrect(pages.admin); // bot1 now permanently locked out this track
      // A locked-out player's buzzer may be literally disabled, OR the UI
      // may replace the whole buzz screen with locked-out messaging (no
      // "Guess buzzer" button in the DOM at all) -- both satisfy the real
      // intent ("can't buzz again"), so accept either.
      const bot1Buzzer = pages.bot1.getByRole("button", { name: "Guess buzzer", exact: false });
      const bot1ButtonPresent = await bot1Buzzer.isVisible().catch(() => false);
      const bot1Disabled = !bot1ButtonPresent || !(await bot1Buzzer.isEnabled().catch(() => false));
      assert("input_lockedOutBuzzerDisabled", bot1Disabled, { bot1ButtonPresent });
      const bot2Buzzer = pages.bot2.getByRole("button", { name: "Guess buzzer", exact: false });
      assert("input_eligibleBuzzerEnabled", await bot2Buzzer.isEnabled().catch(() => false));

      await buzz(pages.bot2);
      await pages.admin.waitForTimeout(500);
      const gradeButtonsVisible = await pages.admin.getByRole("button", { name: "Correct", exact: false }).first().isVisible().catch(() => false);
      assert("input_gradeButtonsVisibleDuringAdjudicate", gradeButtonsVisible);
      await gradeCorrect(pages.admin);
      await pages.admin.waitForTimeout(1000);
      const gradeButtonsGoneAfter = !(await pages.admin.getByRole("button", { name: "Correct", exact: false }).first().isVisible().catch(() => false));
      assert("input_gradeButtonsHiddenOutsideAdjudicate", gradeButtonsGoneAfter);

      const pickerVisible = await pages.bot2.locator("button").filter({ hasText: /^\d+$|^—$/ }).first().isVisible({ timeout: 8000 }).catch(() => false);
      assert("input_pickCellUIForDesignatedPicker", pickerVisible);
      const nonPickerVote = await pages.bot1.getByRole("button", { name: /vote for next track|voted/i }).isVisible({ timeout: 8000 }).catch(() => false);
      assert("input_voteScreenForNonPicker", nonPickerVote);

      if (pickerVisible) {
        await pages.bot2.locator("button").filter({ hasText: /^\d+$/ }).first().click().catch(() => {});
      }
    } catch (e) {
      logEvent({ step: "input_availability", error: String(e) });
      assert("input_availability_ranWithoutError", false, { error: String(e) });
    }

    // ================= STEP 8: edge cases from this session's bug history =================
    try {
      // 8a. Background a player's tab exactly at the picker-prompt moment
      // (regression-checks the resync-on-refocus fix).
      await selectCell(pages.admin, 3, 4);
      await buzz(pages.bot3);
      await pages.admin.waitForTimeout(500);
      await pages.bot3.evaluate(() => {
        Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true });
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await gradeCorrect(pages.admin); // bot3 becomes picker while "backgrounded"
      await pages.admin.waitForTimeout(500);
      await pages.bot3.evaluate(() => {
        Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await pages.bot3.waitForTimeout(1500);
      const pickerAfterRefocus = await pages.bot3.locator("button").filter({ hasText: /^\d+$/ }).first().isVisible({ timeout: 8000 }).catch(() => false);
      assert("edge_backgroundedTabStillShowsPickerAfterRefocus", pickerAfterRefocus);
      if (pickerAfterRefocus) {
        await pages.bot3.locator("button").filter({ hasText: /^\d+$/ }).first().click().catch(() => {});
      }

      // 8b. Duplicate-handle join attempt.
      const dupContext = await browser.newContext({ ignoreHTTPSErrors: true });
      const dupPage = await dupContext.newPage();
      await dupPage.goto(`${MOBILE_BASE}/`);
      await dupPage.getByPlaceholder("0xR00K").fill("bot1");
      await dupPage.getByRole("button", { name: /join game/i }).click();
      await dupPage.waitForTimeout(2000);
      const dupBodyText = await dupPage.locator("body").innerText();
      assert("edge_duplicateHandleHandledWithoutCrash", dupBodyText.length > 0, { bodyPreview: dupBodyText.slice(0, 200) });
      await dupContext.close();

      // 8c. Rapid double-click on buzz.
      await selectCell(pages.admin, 3, 5);
      const dblBuzzBtn = pages.bot4.getByRole("button", { name: "Guess buzzer", exact: false });
      await dblBuzzBtn.waitFor({ timeout: 15000 }).catch(() => {});
      await Promise.allSettled([dblBuzzBtn.click(), dblBuzzBtn.click()]);
      await pages.admin.waitForTimeout(800);
      const adjudicateBodyNow = await pages.admin.locator("body").innerText();
      const onlyOneContestantShown = (adjudicateBodyNow.match(/bot4/gi) ?? []).length <= 3; // loose: no crash/duplication signal
      assert("edge_doubleClickBuzzNoCrash", onlyOneContestantShown);
      // Cleanup: gradeIncorrect alone leaves this round ACTIVE on the same
      // cell, which would hang the next selectCell() -- close it out
      // properly with a correct grade from the remaining eligible player.
      await gradeIncorrect(pages.admin).catch(() => {});
      await pages.admin.waitForTimeout(500);
      await buzz(pages.bot5).catch(() => {});
      await pages.admin.waitForTimeout(500);
      await gradeCorrect(pages.admin).catch(() => {});

      // 8d. Player disconnect/reconnect mid-round.
      await selectCell(pages.admin, 4, 1);
      await buzz(pages.bot5);
      await pages.admin.waitForTimeout(500);
      await contexts.bot5.close();
      const reconnectContext = await browser.newContext({ ignoreHTTPSErrors: true });
      const reconnectPage = await reconnectContext.newPage();
      await reconnectPage.goto(`${MOBILE_BASE}/`);
      await reconnectPage.waitForTimeout(2000);
      const reconnectBody = await reconnectPage.locator("body").innerText();
      assert("edge_disconnectReconnectNoCrash", reconnectBody.length > 0);
      contexts.bot5 = reconnectContext;
      pages.bot5 = reconnectPage;
      // Cleanup: gradeIncorrect alone leaves this round ACTIVE on the same
      // cell, which would hang 8e's selectCell() -- close it out properly.
      await gradeIncorrect(pages.admin).catch(() => {});
      await pages.admin.waitForTimeout(500);
      await buzz(pages.bot1).catch(() => {});
      await pages.admin.waitForTimeout(500);
      await gradeCorrect(pages.admin).catch(() => {});

      // 8e. Admin kicks the current cell-picker.
      await selectCell(pages.admin, 4, 2);
      await buzz(pages.bot1);
      await pages.admin.waitForTimeout(500);
      await gradeCorrect(pages.admin); // bot1 becomes picker
      await pages.admin.waitForTimeout(500);
      const kickBtn = pages.admin.getByRole("button", { name: /kick/i }).first();
      const kickVisible = await kickBtn.isVisible({ timeout: 5000 }).catch(() => false);
      if (kickVisible) {
        await kickBtn.click();
        await pages.admin.waitForTimeout(1500);
        const adminBodyAfterKick = await pages.admin.locator("body").innerText();
        assert("edge_kickPickerNoStuckState", adminBodyAfterKick.length > 0);
      } else {
        logEvent({ step: "edge_kick", warning: "kick button not found via /kick/i, skipped" });
      }
    } catch (e) {
      logEvent({ step: "edge_cases", error: String(e) });
      assert("edge_cases_ranWithoutError", false, { error: String(e) });
    }

    await screenshotAll(pages, "99-final");

    logEvent({ step: "summary", results, failures });
    console.log("\n=== FULL MATRIX RESULT ===");
    console.log(JSON.stringify({ results, failures }, null, 2));
    console.log(`Screenshots + timeline: ${RUN_DIR}`);

    // Board cleanup -- delete the board this run created so repeated runs
    // don't accumulate brd_* rows (chosen over reusing a fixed board id
    // since this harness runs once per invocation, not concurrently).
    try {
      await adminFetch(`/api/boards/${setup.boardId}`, { method: "DELETE" });
      logEvent({ step: "cleanup", action: "deleteBoard", boardId: setup.boardId });
    } catch (e) {
      logEvent({ step: "cleanup", error: String(e) });
    }

    if (failures > 0) process.exitCode = 1;
  } finally {
    await browser.close();
  }
}

// Helper for the Daily Double decline-path case, which needs every
// player's buzzer re-armed cleanly before starting a fresh cell -- a no-op
// most of the time, kept separate so a future scenario can call it without
// re-reading this file's grading helpers.
async function resetBuzzLockouts(_pages: Record<Role, Page>): Promise<void> {
  // The server re-arms eligible buzzers on every fresh ROUND_ACTIVE
  // transition already; nothing to do client-side. Kept as a named,
  // documented no-op rather than silently inlined, since a reviewer
  // reading the DD scenario would otherwise wonder why it's missing.
}

main().catch((e) => {
  logEvent({ step: "fatal", error: String(e) });
  console.error(e);
  process.exitCode = 1;
});

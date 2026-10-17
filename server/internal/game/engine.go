package game

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/anticheat"
	"github.com/s45vprubg/yfitops/server/internal/lyrics"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// engine.go is the CORE game engine (design_doc §3 lifecycle, §9 concurrency).
//
// Concurrency model (§9): every InboundHandler call from a connection goroutine
// pushes a command onto an internal channel; a single Run goroutine consumes
// them and is the ONLY mutator of live state. This gives the "sequential,
// first-packet-wins" guarantee for free — a second simultaneous grade/buzz is
// just the next item in the queue, by which time the state has already moved.
//
// Sanitization (§4A) is enforced centrally in the broadcast helpers: anything
// carrying track metadata (reveal, lyrics, adminView, board) goes only to
// trusted roles (stage/admin); mobile only ever sees sanitized StateData and
// the lockout/buzzResult/voteState control frames.

// TransitionDelay is the "Next track in N" countdown before the queued track
// begins (design_doc §3.9). Exposed for tests to shrink.
var TransitionDelay = 3 * time.Second

// defaultBuzzWindowMs is the buzz-collection window (§4B fairness). All buzzes
// are serialized on the Run loop, so a short window lets contenders that arrived
// within a few ms of each other be gathered and ordered by their §4C
// latency-compensated EffectiveBuzzTime instead of by command-enqueue order.
const defaultBuzzWindowMs = 40

// Config tunes engine behavior. Zero values are sane defaults.
type Config struct {
	SessionID        string
	AdminSecret      string
	SkipThresholdPct int // default 50 (§3.8)
	// RevealIntervalMs / RevealPhase1Ms / RevealAlternate seed the reveal-timing
	// knobs. Zero values fall back to defaults in NewEngine. Alternate defaults
	// to true; use RevealAlternateSet to force it false.
	RevealIntervalMs   int
	RevealPhase1Ms     int
	RevealBlockMs      int
	RevealEaseMs       int
	RevealEaseSet      bool
	RevealAlternate    bool
	RevealAlternateSet bool
	// BuzzWindowMs is the buzz-collection window in ms (§4B fairness). Contenders
	// buzzing within the window are ordered by §4C EffectiveBuzzTime rather than
	// command-enqueue order. Sign convention:
	//   0        => unset; NewEngine substitutes defaultBuzzWindowMs (40).
	//   > 0      => async window of that many ms (production path).
	//   < 0      => resolve immediately/synchronously (a 1-contender window that
	//               resolves inline) — deterministic tests opt out of the timer.
	// A plain 0 cannot mean "immediate" because production callers leave it unset
	// and MUST get the fairness window; hence the negative sentinel.
	BuzzWindowMs int
	// Rand seeds track selection; nil uses a time-seeded source.
	Rand *rand.Rand
}

// command is a unit of work for the single Run loop. fn runs with exclusive
// access to engine state.
type command struct {
	fn   func()
	done chan struct{}
}

// pendingPartial carries the leftover scoring pool after a partial guess (§7).
type pendingPartial struct {
	active    bool
	remaining int // snapshot of leftover pool at the moment of the partial
}

// buzzContender is one eligible buzz gathered during the collection window
// (§4B). effectiveMs is the §4C latency-compensated time; the winner is the
// contender with the minimum effectiveMs (tie-break arrivalMs, then playerID).
type buzzContender struct {
	playerID  string
	connID    string
	arrivalMs int64 // server-stamped arrival; the SOLE ordering input (§4B)
}

// currentPointsFromPool applies the same linear decay as CurrentPoints but with
// explicit ceiling/floor instead of row-derived values. Used after a partial
// shifts the scoring pool. (Cannot live in scoring.go — fixed contract.)
//
// This is the THIRD copy of the §7 decay curve, and the only one on a hot
// display path: web/shared/scoring.ts mirrors it verbatim for the stage's 60fps
// point timer (web/stage/src/views/ActiveRound.tsx), so the two must agree at
// the floor or the projected number disagrees with the score awarded.
// TestCurrentPointsFromPool_* pins the vectors both sides must reproduce.
func currentPointsFromPool(maxP, baseP int, elapsedMs int64) int {
	t := float64(elapsedMs) / 1000.0
	bonus := float64(maxP - baseP)
	switch {
	case t <= DecayHoldSeconds:
		return maxP
	case t >= DecayEndSeconds:
		return baseP
	default:
		span := DecayEndSeconds - DecayHoldSeconds
		frac := 1.0 - (t-DecayHoldSeconds)/span
		// math.Floor, not a bare int() truncation: identical for the
		// non-negative pools this can be called with (PartialAward clamps
		// remaining at 0), but the §7 contract specifies floor and the JS
		// mirror uses Math.floor. Keep the three copies spelled the same way.
		return int(math.Floor(float64(baseP) + bonus*frac))
	}
}

// roundPool returns the round's awardable (ceiling, floor): the post-partial
// pool once a partial has taken its 50, otherwise the row's. It is the single
// source of truth for every pool we put on the wire (trackStart) or show the
// admin, so the number the stage projects at 60fps is the number gradeCorrect
// pays (§5 / BUILD_CONTRACT hard rule 6). Deriving a pool from the row instead
// is what made the stage show 190 and pay 70.
func (e *Engine) roundPool() (maxP, baseP int) {
	if e.partial.active {
		// A prior partial shifted the decay pool: the remaining ceiling with
		// the leftover half of the base as its floor.
		maxP, baseP = e.partial.remaining, BaseValue-PartialPoints
	} else {
		maxP, baseP = MaxPointsForRow(e.curRow), BaseValue
	}
	if baseP > maxP {
		baseP = maxP // defensive: never invert the pool
	}
	return maxP, baseP
}

// Engine implements game.InboundHandler and drives the full lifecycle.
type Engine struct {
	repo           GameRepo
	lock           BuzzLock
	audio          AudioDevice
	lyrics         LyricsProvider
	lyricsStore    LyricsTextStore // optional; DB-first lookup before any live LRCLIB call
	dropPointStore DropPointStore  // optional; nil disables Mark/Jump Drop Point entirely
	bcast          Broadcaster
	gate           *anticheat.NonceGate
	cfg            Config
	rng            *rand.Rand

	cmds       chan command
	seq        uint64
	roleSetter RoleSetter // promotes a conn's role in the transport on Hello

	// ---- live state (Run-goroutine-owned) ----
	state   protocol.GameState
	board   *Board
	reg     *registry
	session *Session

	curCell  *Cell
	curTrack *Track
	// curDropPointMs caches e.curTrack's drop-point mark (nil = none known),
	// refreshed whenever curTrack is set to a new track and whenever
	// onAdminSetDropPoint successfully writes one — avoids a DB round-trip on
	// every onAdminJumpToDropPoint call and every admin position broadcast.
	curDropPointMs *int64
	curRow         int
	roundKey       string // BuzzLock key for the active buzz round
	trackStartMs   int64  // server epoch ms when the current track started
	pausedAtMs     int64  // playback position when paused on a buzz
	buzzWinner     string // playerID currently holding the lock
	partial        pendingPartial

	// ---- buzz-collection window (§4B fairness, Run-loop-owned) ----
	// buzzWindowMs is the resolved window length. >0 opens an async collection
	// window; <=0 resolves each buzz immediately (a 1-contender window). All of
	// the fields below are single-writer (Run loop only) — no locks needed.
	buzzWindowMs   int
	buzzWindowOpen bool
	buzzContenders []buzzContender

	cellPicker string // playerID who earns next cell selection ("" = admin only)

	// pendingPick holds the cell e.cellPicker chose during KARAOKE, nil until
	// they pick. karaokeReady records that the OTHER half of the AND-gate
	// (skip vote threshold reached, or the track ended naturally) already
	// fired. Whichever of the two resolves second triggers beginTransition —
	// see onPlayerSelectCell / karaokeConditionMet. Both are reset whenever a
	// new selection actually starts (selectCell) and whenever a fresh karaoke
	// phase begins (enterKaraoke).
	pendingPick  *cellCoord
	karaokeReady bool

	// skip voting (§3.8)
	votingPool map[string]bool // playerIDs eligible to vote this karaoke phase
	votes      map[string]bool // playerIDs who have voted

	// daily double (§7)
	ratingPool map[string]bool
	ratings    map[string]int // playerID -> stars
	// lastScorer is the playerID who most recently earned GAMEPLAY points (buzz
	// win, partial, or a prior Daily Double payout) — unlike roundWinner, never
	// reset per-round. Drives Daily Double contestant selection. Manual
	// admin.award adjustments do NOT update this (see award()).
	lastScorer string
	// cellVisits counts admin selections per cell ("row:col" key), so a cell's
	// DDTriggerVisit can fire on an exact later visit rather than every pick.
	cellVisits map[string]int
	// ddOffer is non-nil only during a Daily Double's offer/song-pick sub-phase
	// (before a track starts playing); nil once the performance begins.
	ddOffer *dailyDoubleOffer
	// ddBucketOriginal snapshots e.board.DDBucket exactly as it was when the
	// board was last attached (SetBoard / ReloadBoard / Run's initial
	// LoadBoard) — i.e. the curated Postgres bucket, before any Daily Double
	// accept drains it with a no-replacement draw. ResetToLobby restores
	// DDBucket from this snapshot, so a "New Game, same board" cycle doesn't
	// permanently shrink the bucket game over game (dd-eng-6, QA sweep 6).
	ddBucketOriginal []*Track

	// volumePct is the operating playback volume (0-100, default 100),
	// settable any time via admin.setVolume. It is the level the Daily
	// Double fade-out restores to once a performance ends — not DD-specific
	// itself, just introduced alongside that feature.
	volumePct int
	// volumeCmds serializes EVERY outbound AudioDevice.SetVolume call —
	// admin.setVolume's live-apply, the fade's stepped ramp, and the fade's
	// final restore — through the single writer goroutine runVolumeWriter
	// starts (s8eng-05). Before this, each call site fired its own ad hoc
	// goroutine straight at the live device with no ordering guarantee, so
	// two in-flight HTTP calls could race and land in the wrong order. A
	// buffered channel + one consumer gives strict FIFO application that
	// matches call-site/intent order without needing per-call sequence
	// numbers.
	volumeCmds chan int
	// ddFadingRoundKey guards fadeOutAndFinishDailyDouble against
	// double-firing AND against a stale fade from an earlier, now-ended
	// performance blocking or misfiring against a later, unrelated one
	// (s8eng-03). It holds the e.roundKey of whichever Daily Double
	// performance currently has a graceful fade-out in flight (from either
	// the automatic all-rated completion or the admin.endDailyDouble
	// button), "" when none is in flight. e.roundKey is already minted
	// fresh per DD performance (startDailyDoublePerformance), so comparing
	// against the CURRENT e.roundKey — not a bare bool — lets a new
	// performance's own fade proceed even while an old, already-abandoned
	// fade's continuation is still pending, and lets that stale
	// continuation recognize the round has moved on and bail instead of
	// finishing the wrong performance. A Force End Round/
	// admin.skipDailyDouble that already finished the round in the
	// meantime (both stay instant, untouched by this field) still makes
	// the stale continuation a safe no-op via its own e.roundKey check.
	ddFadingRoundKey string

	// spotifyAuthed records that the admin completed Spotify OAuth, so a stage
	// that connects AFTER the OAuth dance still learns to initialize the Web
	// Playback SDK (it then fetches the live token from /api/spotify/token).
	spotifyAuthed bool

	// Server-authoritative letter reveal (see reveal.go). revealCfg holds the
	// live-tunable knobs (applied next round); rc is the running reveal clock
	// for the current round; revealTimer is its off-loop ticker.
	revealCfg   revealConfig
	rc          revealClock
	revealTimer *time.Timer
	// revealLocked: the reveal got close enough to complete that buzzing is
	// closed and points are zeroed for the rest of this round. Reset each track.
	revealLocked bool
	// roundWinner is the handle of whoever guessed correctly this round ("" =
	// nobody won). Set in gradeCorrect, cleared at startTrack; drives the
	// karaoke winner banner (so it never defaults to the scoreboard leader).
	roundWinner string

	// connIP maps a live connID -> client IP (host only) for anti-cheat
	// telemetry (shared-IP detection). Run-goroutine-owned.
	connIP map[string]string

	// telemetryDirty coalesces heartbeat-driven telemetry: onHeartbeat sets it
	// instead of broadcasting inline (which is O(P^2) on the Run loop and
	// floodable). A fast ticker flushes it at most once per second. Run-loop-owned.
	telemetryDirty bool
	// scoreboardDirty/boardDirty: the same coalescing applied to onHello's
	// per-join broadcasts (join-burst fix). A stress test adding players in
	// batches found admin becoming unresponsive — onHello was calling
	// broadcastScoreboard/broadcastBoard/broadcastTelemetry synchronously and
	// unconditionally on EVERY join, the identical O(P^2)-floodable pattern
	// already fixed for heartbeats, just never applied here. Set instead of
	// broadcast inline; the same 1s ticker that flushes telemetryDirty now
	// flushes these too, so a burst of N joins costs one flush, not N.
	scoreboardDirty bool
	boardDirty      bool

	// lyricCache memoizes LRCLIB results by artist|song|dur so a track's lyrics
	// are fetched once (prefetched at startTrack) and served instantly at
	// karaoke — no loading spinner. A cached entry may be an empty slice
	// (known to have no synced lyrics). Guarded by lyricMu (touched off-loop).
	lyricMu    sync.Mutex
	lyricCache map[string]*lyricEntry

	// soloKaraoke is the live state for the independent Solo Karaoke mode
	// (see solo_karaoke.go) — straight playback with no guessing/scoring,
	// mutually exclusive with the trivia state machine via the mode-lock
	// gate (modeLockOwner). Run-goroutine-owned like every other live field.
	soloKaraoke soloKaraokeState
}

// NewEngine wires the engine to its dependencies (all injected for testing).
// lyricsStore is optional (nil is fine, e.g. no Postgres/in-memory repo) —
// nil means every lyric lookup falls back straight to lyrics.Fetch, exactly
// matching pre-cache behavior. dropPointStore is likewise optional — nil
// disables Mark/Jump Drop Point entirely.
func NewEngine(repo GameRepo, lock BuzzLock, audio AudioDevice, lyrics LyricsProvider, lyricsStore LyricsTextStore, dropPointStore DropPointStore, bcast Broadcaster, gate *anticheat.NonceGate, cfg Config) *Engine {
	rng := cfg.Rand
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if cfg.SkipThresholdPct == 0 {
		cfg.SkipThresholdPct = 50
	}
	if cfg.SessionID == "" {
		cfg.SessionID = "session"
	}
	// Resolve the buzz-collection window (§4B). 0 => unset => default. A negative
	// value is the "resolve immediately" sentinel (kept as-is; <=0 is the
	// synchronous path in onBuzz).
	buzzWindowMs := cfg.BuzzWindowMs
	if buzzWindowMs == 0 {
		buzzWindowMs = defaultBuzzWindowMs
	}
	// Seed the reveal-timing knobs from config, falling back to defaults.
	revCfg := defaultRevealConfig()
	if cfg.RevealIntervalMs > 0 {
		revCfg.IntervalMs = cfg.RevealIntervalMs
	}
	if cfg.RevealPhase1Ms > 0 {
		revCfg.Phase1Ms = cfg.RevealPhase1Ms
	}
	if cfg.RevealBlockMs > 0 {
		revCfg.BlockMs = cfg.RevealBlockMs
	}
	if cfg.RevealEaseSet {
		revCfg.EaseMs = cfg.RevealEaseMs
	}
	if cfg.RevealAlternateSet {
		revCfg.Alternate = cfg.RevealAlternate
	}
	return &Engine{
		repo:           repo,
		lock:           lock,
		audio:          audio,
		lyrics:         lyrics,
		lyricsStore:    lyricsStore,
		dropPointStore: dropPointStore,
		bcast:          bcast,
		gate:           gate,
		cfg:            cfg,
		rng:            rng,
		cmds:           make(chan command, 256),
		state:          protocol.StateLobby,
		reg:            newRegistry(),
		session:        &Session{ID: cfg.SessionID, SkipThresholdPct: cfg.SkipThresholdPct, State: string(protocol.StateLobby)},
		revealCfg:      revCfg.clamp(),
		connIP:         map[string]string{},
		lyricCache:     map[string]*lyricEntry{},
		buzzWindowMs:   buzzWindowMs,
		cellVisits:     map[string]int{},
		volumePct:      100,
		volumeCmds:     make(chan int, 64),
		soloKaraoke:    soloKaraokeState{index: -1},
	}
}

// Run is the single command loop (§9). All state mutation happens here. It
// returns when ctx is cancelled. The engine must be running before inbound
// calls do useful work; OnConnect/OnMessage/OnDisconnect enqueue onto cmds.
func (e *Engine) Run(ctx context.Context) error {
	if e.board == nil {
		if b, err := e.repo.LoadBoard(ctx, e.cfg.SessionID); err == nil && b != nil {
			e.board = b
			e.snapshotDDBucket()
		}
	}
	_ = e.repo.CreateSession(ctx, e.session)
	// s8eng-05: one writer owns every outbound SetVolume call for this
	// engine's lifetime, serializing admin.setVolume / the fade's stepped
	// ramp / the fade's restore against each other.
	go e.runVolumeWriter(ctx)
	telemetryTicker := time.NewTicker(60 * time.Second)
	defer telemetryTicker.Stop()
	// Fast ticker flushes coalesced heartbeat telemetry at most once per second
	// (onHeartbeat sets telemetryDirty instead of broadcasting inline, which is
	// O(P^2) on this loop and floodable).
	telemetryFlush := time.NewTicker(1 * time.Second)
	defer telemetryFlush.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c := <-e.cmds:
			// A panic in one handler must NOT take down the single Run goroutine
			// (which would kill every connection and the whole game). Recover,
			// log, and drop that command; the loop keeps serving.
			e.runCommand(c)
		case <-telemetryFlush.C:
			if e.telemetryDirty {
				e.telemetryDirty = false
				e.broadcastTelemetry()
			}
			if e.scoreboardDirty {
				e.scoreboardDirty = false
				e.broadcastScoreboard()
			}
			if e.boardDirty {
				e.boardDirty = false
				e.broadcastBoard()
			}
		case <-telemetryTicker.C:
			e.broadcastTelemetry()
		}
	}
}

// runCommand executes one queued command with panic recovery so a bug in a
// single handler degrades to a logged, dropped command instead of crashing the
// engine goroutine (and with it every connection). done is still closed so any
// synchronous caller (submitSync) is never left blocked.
func (e *Engine) runCommand(c command) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[engine] PANIC in command handler (recovered): %v\n%s", r, debug.Stack())
		}
		if c.done != nil {
			close(c.done)
		}
	}()
	c.fn()
}

// SetBoard injects a board directly (test/admin bootstrap). Safe before Run.
func (e *Engine) SetBoard(b *Board) {
	e.board = b
	e.snapshotDDBucket()
}

// snapshotDDBucket records the freshly-attached board's DDBucket contents as
// the "pristine" copy ResetToLobby restores from (dd-eng-6, QA sweep 6). Call
// this every time e.board is (re)assigned to a new *Board.
func (e *Engine) snapshotDDBucket() {
	if e.board == nil {
		e.ddBucketOriginal = nil
		return
	}
	e.ddBucketOriginal = append([]*Track{}, e.board.DDBucket...)
}

// ReloadBoard atomically replaces the active board via the Run loop's command
// channel. Safe to call from any goroutine (e.g., the admin REST handler).
// After replacement, broadcasts the new grid to stage/admin clients.
func (e *Engine) ReloadBoard(b *Board) {
	e.submit(func() {
		e.board = b
		e.snapshotDDBucket()
		e.broadcastBoard()
	})
}

// StartGame transitions the engine from LOBBY to BOARD. Returns an error if no
// board is attached or the engine is not in LOBBY state. Safe from any goroutine.
func (e *Engine) StartGame() error {
	type result struct{ err error }
	ch := make(chan result, 1)
	e.cmds <- command{fn: func() {
		if e.board == nil {
			ch <- result{fmt.Errorf("no board attached")}
			return
		}
		// Mode-lock (solo_karaoke.go): trivia cannot start while Solo Karaoke
		// is active. Released only by an explicit admin.soloKaraoke.stop or
		// queue exhaustion — never by navigation/tab-close (every admin tab
		// reflects the same server-side lock via the modeLock broadcast).
		if e.soloKaraoke.active {
			ch <- result{fmt.Errorf("solo karaoke is active — stop it before starting a trivia game")}
			return
		}
		if e.state != protocol.StateLobby {
			ch <- result{fmt.Errorf("game already started (state: %s)", e.state)}
			return
		}
		e.assignDailyDoubles()
		e.transitionTo(protocol.StateBoard)
		e.broadcastBoard()
		e.broadcastModeLock()
		ch <- result{}
	}}
	return (<-ch).err
}

// ResetToLobby transitions the engine from GAME_OVER back to LOBBY, clearing
// all game state (scores, played tracks, round state) for a fresh start.
// Safe from any goroutine.
func (e *Engine) ResetToLobby() error {
	type result struct{ err error }
	ch := make(chan result, 1)
	e.cmds <- command{fn: func() {
		if e.state != protocol.StateGameOver {
			ch <- result{fmt.Errorf("can only reset from GAME_OVER (state: %s)", e.state)}
			return
		}
		// Clear round state
		e.clearReveal()
		e.curCell = nil
		e.curTrack = nil
		e.curRow = 0
		e.roundKey = ""
		e.trackStartMs = 0
		e.pausedAtMs = 0
		e.buzzWinner = ""
		e.buzzWindowOpen = false
		e.buzzContenders = nil
		e.cellPicker = ""
		e.votingPool = nil
		e.votes = nil
		e.ratingPool = nil
		e.ratings = nil
		e.lastScorer = ""
		e.cellVisits = map[string]int{}
		e.ddOffer = nil
		// Reset all player scores
		for _, p := range e.reg.players {
			p.Score = 0
			p.GuessedThisTrack = false
			p.IdleRounds = 0
		}
		// Reset board track played state, but KEEP the board loaded so
		// New Game -> Start Game works with the same board (previously the board
		// was unloaded here, forcing the admin to re-attach before starting).
		if e.board != nil {
			for _, row := range e.board.Cells {
				for _, cell := range row {
					if cell == nil {
						continue
					}
					for _, t := range cell.Tracks {
						t.Played = false
					}
				}
			}
			// dd-eng-6 (QA sweep 6): restore DDBucket to its board-load-time
			// snapshot. It's board-level state separate from the per-cell Played
			// flags reset above, and it drains via a no-replacement draw in
			// onDailyDoubleDecision's accept path with nothing else ever
			// refilling it — without this, several "New Game, same board"
			// cycles in a row would permanently exhaust it even though the
			// admin believes each New Game gave them a clean slate.
			e.board.DDBucket = append([]*Track{}, e.ddBucketOriginal...)
		}
		// Persist the cleared state so a restart after New Game doesn't
		// resurrect the previous game's consumed tracks (best-effort).
		_ = e.repo.ClearPlayed(context.Background(), e.cfg.SessionID)
		e.transitionTo(protocol.StateLobby)
		e.broadcastScoreboard()
		e.broadcastBoard()
		ch <- result{}
	}}
	return (<-ch).err
}

// consumeTrack marks the current track as played (removing it from its cell
// pool, §7) AND persists that consumption so a mid-game server restart won't
// re-offer it. Persistence is best-effort, same fire-and-forget style as
// SaveScore. Safe to call when there is no current track.
func (e *Engine) consumeTrack() {
	if e.curTrack == nil {
		return
	}
	e.curTrack.Played = true
	_ = e.repo.MarkTrackPlayed(context.Background(), e.cfg.SessionID, e.curTrack.ID)
}

// RoleSetter lets the engine promote a connection's authenticated role in the
// transport layer after a validated Hello. The transport (*transport.Hub)
// defaults every new connection to mobile (the safe default, §4A) and only the
// engine knows the real role once Hello is validated — so without this hook,
// role-scoped broadcasts (Broadcast(stage/admin)) would never reach anyone and
// admin actions would be impossible end-to-end. Optional: tests leave it nil
// and pass the role directly into OnMessage.
type RoleSetter interface {
	SetRole(connID string, role protocol.Role)
}

// SetRoleSetter wires the transport hub so the engine can promote roles on
// Hello. Call once before Run; *transport.Hub satisfies RoleSetter.
func (e *Engine) SetRoleSetter(rs RoleSetter) { e.roleSetter = rs }

// CONTRACT-QUESTION: SMsgSpotifyToken is a new message type needed for pushing
// OAuth tokens to the Stage client. It is defined here (not in protocol.go)
// because protocol.go is a fixed contract. If accepted, it should be moved there.
const smsgSpotifyToken protocol.ServerMsgType = "spotifyToken"

// CONTRACT-QUESTION: partialReveal signals the stage to fully reveal one field
// (artist or song) after a partial grade, so the animation settles that field.
const smsgPartialReveal protocol.ServerMsgType = "partialReveal"

// CONTRACT-QUESTION: gradeResult tells the buzz winner their verdict after
// adjudication so the mobile can show an appropriate message.
const smsgGradeResult protocol.ServerMsgType = "gradeResult"

// CONTRACT-QUESTION: the redesigned Daily Double (§7 sidenote) needs a
// contestant accept/decline + song-pick round trip that protocol.go has no
// equivalent for (its only Daily Double message, CMsgRate, is the crowd
// rating — unrelated). Defined locally per the fixed-contract process; not in
// protocol.go. Discuss with the project owner for promotion on a version bump;
// documented in docs/CHANGELOG.md.
const (
	// cmsgDailyDoubleDecision: {accept bool} — the contestant's own connection only.
	cmsgDailyDoubleDecision protocol.ClientMsgType = "dailyDouble.decision"
	// cmsgDailyDoubleChoose: {trackID string} — the contestant's own connection only.
	cmsgDailyDoubleChoose protocol.ClientMsgType = "dailyDouble.choose"
	// cmsgAdminSkipDailyDouble: abandon/abort the current Daily Double,
	// whichever sub-phase it's in — an offer/song-pick still deciding (treated
	// as a decline) OR a performance already live (finishes it exactly like a
	// natural song-end). Shares its logic with CMsgAdminEndRound's own
	// Daily-Double branch via skipDailyDouble() (dd-eng-5, QA sweep 6).
	cmsgAdminSkipDailyDouble protocol.ClientMsgType = "admin.skipDailyDouble"

	// smsgDailyDoublePerformer: {playerID, handle, performing} — sanitized (no
	// track data), broadcast to ALL roles so stage/mobile can show "X is doing
	// a Daily Double" during the offer/pick sub-phase and "X is performing"
	// once a song starts.
	smsgDailyDoublePerformer protocol.ServerMsgType = "dailyDouble.performer"
	// smsgDailyDoubleResult: {avgStars, points} — sanitized, broadcast to ALL
	// roles once the average rating + payout are final.
	smsgDailyDoubleResult protocol.ServerMsgType = "dailyDouble.result"
)

// CONTRACT-QUESTION (§4A exception, owner-approved — see docs/CHANGELOG.md):
// smsgDailyDoubleOffer sends track title/artist to a MOBILE client, which the
// locked sanitization rule normally forbids outright. This is a deliberate,
// narrowly-scoped exception: the payload goes ONLY to the Daily Double
// contestant's own connection(s), ONLY at the instant they accept, for songs
// they are seconds from hearing regardless of which they choose. It is a
// single just-in-time push — never prefetched, never broadcast, and the
// mobile client holds it only in transient component state (never
// localStorage/sessionStorage), cleared the instant the phase changes. No
// other mobile client ever receives this message.
const smsgDailyDoubleOffer protocol.ServerMsgType = "dailyDouble.offer"

type dailyDoubleDecisionData struct {
	Accept bool `json:"accept"`
}

type dailyDoubleChooseData struct {
	TrackID string `json:"trackID"`
}

type dailyDoublePerformerData struct {
	PlayerID   string `json:"playerID"`
	Handle     string `json:"handle"`
	Performing bool   `json:"performing"`
}

type dailyDoubleSongChoice struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

type dailyDoubleOfferData struct {
	Songs []dailyDoubleSongChoice `json:"songs"`
}

type dailyDoubleResultData struct {
	AvgStars float64 `json:"avgStars"`
	Points   int     `json:"points"`
}

// dailyDoubleOffer tracks a pending Daily Double before a track starts
// playing (offer + song-pick sub-phases). Non-nil only during that window.
type dailyDoubleOffer struct {
	cellRow, cellCol int
	contestantID     string
	candidates       []*Track
}

// CONTRACT-QUESTION: player-driven cell picking lets whoever last scored
// (e.cellPicker) choose the next board cell from their own phone instead of
// the admin always picking. protocol.go has no equivalent message — defined
// locally per the fixed-contract process; discuss with the project owner for
// promotion on a version bump; documented in docs/CHANGELOG.md.
const (
	// cmsgPlayerSelectCell: {row, col} — the designated picker's own
	// connection only (e.cellPicker). Legal during KARAOKE (part of the
	// AND-gate with the skip vote / track-end, see karaokeConditionMet) and
	// during BOARD (the simpler case right after a Daily Double, which
	// transitions straight to BOARD with no karaoke/vote phase at all).
	cmsgPlayerSelectCell protocol.ClientMsgType = "player.selectCell"

	// smsgPickerStatus: {pickerHandle, hasPicked} — sanitized (no board or
	// track data), broadcast to ALL roles so mobile can show "waiting on
	// {handle} to pick" and the picker's own device knows when its pick has
	// registered.
	smsgPickerStatus protocol.ServerMsgType = "pickerStatus"

	// smsgPickableBoard: the already-§4A-safe BoardData shape (category,
	// points, exhausted — no track metadata), sent ONLY to the designated
	// picker's own connection(s) so their phone can render a tappable grid.
	// No other mobile client ever receives this.
	smsgPickableBoard protocol.ServerMsgType = "pickableBoard"
)

// cellCoord is a 1-indexed (row, col) board coordinate — used to hold the
// picker's chosen cell between onPlayerSelectCell and the AND-gated
// beginTransition that actually starts it (see pendingPick).
type cellCoord struct {
	Row, Col int
}

type playerSelectCellData struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type pickerStatusData struct {
	PickerID     string `json:"pickerID"`
	PickerHandle string `json:"pickerHandle"`
	HasPicked    bool   `json:"hasPicked"`
}

// PushSpotifyToken sends the access token to all connected Stage clients so
// they can initialize the Web Playback SDK without being in the OAuth loop.
func (e *Engine) PushSpotifyToken(token string) {
	e.submit(func() {
		e.spotifyAuthed = true // remember so a stage connecting LATER also learns
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgSpotifyToken, map[string]string{"token": token}))
	})
}

// MarkSpotifyAuthed flags Spotify as already authenticated WITHOUT broadcasting
// (no stage may be connected yet). Used at boot when a persisted refresh token
// is restored: without this, sendFullSync would never tell a connecting stage
// to initialize the Web Playback SDK, so no playback device registers and the
// server hits NO_ACTIVE_DEVICE. Safe from any goroutine.
func (e *Engine) MarkSpotifyAuthed() {
	e.submit(func() { e.spotifyAuthed = true })
}

// submit enqueues fn for the Run loop. It does not wait. If the (generously
// buffered) command channel is full it applies blocking backpressure: the send
// blocks until the loop drains one. Running fn inline from a non-Run goroutine
// is NOT safe (it would race the single-writer state), so we never do that.
func (e *Engine) submit(fn func()) {
	e.cmds <- command{fn: fn}
}

// submitSync enqueues fn and waits for the loop to finish it. Used by tests to
// observe state deterministically.
func (e *Engine) submitSync(fn func()) {
	done := make(chan struct{})
	e.cmds <- command{fn: fn, done: done}
	<-done
}

// ---------------------------------------------------------------------------
// InboundHandler (design_doc ports.go) — these are called from many connection
// goroutines and only enqueue work; they never touch state directly.
// ---------------------------------------------------------------------------

// OnConnect registers a bare connection before Hello (ports.InboundHandler).
func (e *Engine) OnConnect(connID, remoteIP string) {
	e.submit(func() {
		e.reg.addConn(connID)
		if remoteIP != "" {
			e.connIP[connID] = remoteIP
		}
	})
}

// OnMessage forwards a decoded client frame. arrivalUnixMs is the SERVER
// arrival clock used for buzz ordering (§4B).
func (e *Engine) OnMessage(connID string, role protocol.Role, env protocol.ClientEnvelope, arrivalUnixMs int64) {
	e.submit(func() { e.dispatch(connID, role, env, arrivalUnixMs) })
}

// OnDisconnect removes a connection and recalculates the active pool (§3.8).
func (e *Engine) OnDisconnect(connID string) {
	e.submit(func() {
		delete(e.connIP, connID)
		playerID, fullyGone := e.reg.removeConn(connID)
		if playerID != "" && fullyGone {
			// Drop from active vote/rating pools and recompute thresholds (§3.8).
			delete(e.votingPool, playerID)
			delete(e.ratingPool, playerID)
			delete(e.ratings, playerID) // keep ratings in sync with pool (§3.8)
			if p := e.reg.players[playerID]; p != nil {
				p.Active = false
			}
			if e.state == protocol.StateKaraoke {
				e.evaluateSkipVotes()
			}
			if e.state == protocol.StateDailyDouble {
				if e.ddOffer != nil && e.ddOffer.contestantID == playerID {
					// The contestant dropped mid-decision: don't stall the game
					// waiting for a phone that's gone. Full disconnect is a
					// stronger signal than "took too long to decide", so this is
					// distinct from the deliberate no-auto-timeout choice for a
					// merely-slow response.
					e.declineDailyDouble()
				} else {
					e.checkDailyDoubleComplete()
				}
			}
			// s8eng-02: mirror the DD-contestant-disconnect handling above — a
			// cell-picker who fully disconnects mid-pick must not leave stale
			// "waiting on X" state that can never resolve on its own (the only
			// prior escape valve was an admin noticing and firing admin.select).
			if playerID == e.cellPicker {
				wasReady := e.karaokeReady
				e.cellPicker = ""
				e.pendingPick = nil
				e.karaokeReady = false
				e.broadcastPickerStatus()
				if wasReady && e.state == protocol.StateKaraoke {
					// The OTHER AND-gate half had already fired (skip-vote
					// threshold reached / track ended) before the picker
					// vanished, so nothing will ever supply a pendingPick again.
					// Fall through to the same no-valid-pick continuation
					// beginTransition's own stalled-pick handling uses, instead
					// of leaving this in limbo until the next admin.select.
					e.beginTransition()
				}
			}
			e.broadcastTelemetry()
		}
	})
}

// dispatch routes one client frame to its handler. Runs on the Run goroutine.
func (e *Engine) dispatch(connID string, role protocol.Role, env protocol.ClientEnvelope, arrivalMs int64) {
	switch env.Type {
	case protocol.CMsgHello:
		e.onHello(connID, env)
	case protocol.CMsgHeartbeat:
		e.onHeartbeat(connID, env)
	case protocol.CMsgResync:
		e.sendFullSync(connID, role)
	case protocol.CMsgBuzz:
		e.onBuzz(connID, env, arrivalMs)
	case protocol.CMsgVote:
		e.onVote(connID, env)
	case protocol.CMsgRate:
		e.onRate(connID, env)
	case cmsgDailyDoubleDecision:
		e.onDailyDoubleDecision(connID, env)
	case cmsgDailyDoubleChoose:
		e.onDailyDoubleChoose(connID, env)
	case cmsgPlayerSelectCell:
		e.onPlayerSelectCell(connID, env)

	// Admin actions — every one validates the admin role on the connection.
	case protocol.CMsgAdminGrade,
		protocol.CMsgAdminSelect,
		protocol.CMsgAdminPlayback,
		protocol.CMsgAdminAward,
		protocol.CMsgAdminKick,
		protocol.CMsgAdminReveal,
		protocol.CMsgAdminEndRound,
		protocol.CMsgAdminSetThresh,
		protocol.CMsgAdminEndGame,
		cmsgAdminSetRevealCfg,
		cmsgAdminSkipDailyDouble,
		cmsgAdminSeek,
		cmsgAdminSetDropPoint,
		cmsgAdminJumpToDropPoint,
		cmsgAdminSetVolume,
		cmsgAdminEndDailyDouble,
		cmsgAdminSoloKaraokePlay,
		cmsgAdminSoloKaraokePause,
		cmsgAdminSoloKaraokeResume,
		cmsgAdminSoloKaraokeStop,
		cmsgAdminSoloKaraokeNext,
		cmsgAdminSoloKaraokePrev,
		cmsgAdminSoloKaraokeSeek,
		cmsgAdminSoloKaraokeSetShuffle,
		cmsgAdminSoloKaraokeSetRepeat:
		if role != protocol.RoleAdmin {
			e.sendError(connID, "forbidden", "admin role required")
			return
		}
		e.dispatchAdmin(connID, env)

	case protocol.CMsgStagePlayerState,
		protocol.CMsgStageDeviceReady:
		if role != protocol.RoleStage && role != protocol.RoleAdmin {
			e.sendError(connID, "forbidden", "stage role required")
			return
		}
		switch env.Type {
		case protocol.CMsgStagePlayerState:
			e.onStagePlayerState(connID, env)
		case protocol.CMsgStageDeviceReady:
			e.onStageDeviceReady(connID, env)
		}
	default:
		e.sendError(connID, "unknown", fmt.Sprintf("unhandled type %q", env.Type))
	}
}

func (e *Engine) dispatchAdmin(connID string, env protocol.ClientEnvelope) {
	switch env.Type {
	case protocol.CMsgAdminGrade:
		e.onGrade(connID, env)
	case protocol.CMsgAdminSelect:
		e.onAdminSelect(connID, env)
	case protocol.CMsgAdminPlayback:
		e.onAdminPlayback(connID, env)
	case protocol.CMsgAdminAward:
		e.onAdminAward(connID, env)
	case protocol.CMsgAdminKick:
		e.onAdminKick(connID, env)
	case protocol.CMsgAdminReveal:
		e.adminReveal()
	case protocol.CMsgAdminEndRound:
		e.endRound()
	case protocol.CMsgAdminSetThresh:
		e.onAdminSetThresh(connID, env)
	case cmsgAdminSetRevealCfg:
		e.onAdminSetRevealCfg(connID, env)
	case cmsgAdminSkipDailyDouble:
		e.skipDailyDouble()
	case cmsgAdminSeek:
		e.onAdminSeek(connID, env)
	case cmsgAdminSetDropPoint:
		e.onAdminSetDropPoint(connID, env)
	case cmsgAdminJumpToDropPoint:
		e.onAdminJumpToDropPoint(connID, env)
	case cmsgAdminSetVolume:
		e.onAdminSetVolume(connID, env)
	case cmsgAdminEndDailyDouble:
		e.onAdminEndDailyDouble(connID, env)
	case cmsgAdminSoloKaraokePlay:
		e.onAdminSoloKaraokePlay(connID, env)
	case cmsgAdminSoloKaraokePause:
		e.onAdminSoloKaraokePause(connID, env)
	case cmsgAdminSoloKaraokeResume:
		e.onAdminSoloKaraokeResume(connID, env)
	case cmsgAdminSoloKaraokeStop:
		e.onAdminSoloKaraokeStop(connID, env)
	case cmsgAdminSoloKaraokeNext:
		e.onAdminSoloKaraokeNext(connID, env)
	case cmsgAdminSoloKaraokePrev:
		e.onAdminSoloKaraokePrev(connID, env)
	case cmsgAdminSoloKaraokeSeek:
		e.onAdminSoloKaraokeSeek(connID, env)
	case cmsgAdminSoloKaraokeSetShuffle:
		e.onAdminSoloKaraokeSetShuffle(connID, env)
	case cmsgAdminSoloKaraokeSetRepeat:
		e.onAdminSoloKaraokeSetRepeat(connID, env)
	case protocol.CMsgAdminEndGame:
		e.transitionTo(protocol.StateGameOver)
		e.persistScores()
		// trivia -> GAME_OVER releases the mode-lock (back to "none" unless
		// Solo Karaoke somehow claimed it in the meantime, which it cannot
		// while e.soloKaraoke.active implies trivia was never holding the
		// lock in the first place — see modeLockOwner).
		e.broadcastModeLock()
	}
}

// ---------------------------------------------------------------------------
// Hello / heartbeat / sync (§3.1, §3.2, §4C, §9)
// ---------------------------------------------------------------------------

func (e *Engine) onHello(connID string, env protocol.ClientEnvelope) {
	var h protocol.HelloData
	if err := json.Unmarshal(env.Data, &h); err != nil {
		e.sendError(connID, "badHello", err.Error())
		return
	}
	// Admin and Stage roles are gated by the shared secret (§9 auth).
	// Constant-time compare: a plain != leaks timing information about how
	// many leading bytes of the secret matched.
	if (h.Role == protocol.RoleAdmin || h.Role == protocol.RoleStage) && e.cfg.AdminSecret != "" &&
		subtle.ConstantTimeCompare([]byte(h.AdminSecret), []byte(e.cfg.AdminSecret)) != 1 {
		e.sendError(connID, "forbidden", "bad admin secret")
		return
	}
	p, _ := e.reg.resolvePlayer(connID, h)
	if p.Banned {
		e.sendError(connID, "banned", "you are banned")
		return
	}
	// Promote the connection's role in the transport now that Hello is validated
	// (§4A). Until this call the transport treats the conn as mobile, so this is
	// what makes role-scoped broadcasts and admin actions work end-to-end.
	if e.roleSetter != nil {
		e.roleSetter.SetRole(connID, h.Role)
	}
	// Players joining during the voting phase do NOT count toward the active
	// pool for that vote (§3.8). Otherwise a fresh mobile player is active.
	if h.Role == protocol.RoleMobile {
		p.Active = true
		p.IdleRounds = 0
	}
	e.bcast.SendTo(connID, e.envelope(protocol.SMsgWelcome, protocol.WelcomeData{
		PlayerID: p.ID, Role: h.Role, Nonce: e.gate.Current(),
	}))
	e.sendFullSync(connID, h.Role)
	// Join-burst fix: coalesce instead of broadcasting inline, same pattern
	// as onHeartbeat's telemetryDirty. The joining connection already has
	// its own current board/scoreboard via sendFullSync above; these flags
	// only cover updating EVERYONE ELSE's view (e.g. "player count went
	// up"), which doesn't need to be instant and must not cost O(P) per
	// join when many joins land in a burst.
	e.scoreboardDirty = true
	e.boardDirty = true
	if h.Role == protocol.RoleAdmin || h.Role == protocol.RoleMobile {
		e.telemetryDirty = true
	}
}

func (e *Engine) onHeartbeat(connID string, env protocol.ClientEnvelope) {
	var hb struct {
		ClientTime int64 `json:"clientTime"`
		RTTMs      int   `json:"rttMs,omitempty"`
	}
	_ = json.Unmarshal(env.Data, &hb)
	now := nowMs()
	if p := e.reg.playerForConn(connID); p != nil {
		p.IdleRounds = 0
		if e.reg.isMobile(p.ID) {
			p.Active = true
		}
		if hb.RTTMs > 0 {
			if p.RTTMs == 0 {
				p.RTTMs = hb.RTTMs
			} else {
				p.RTTMs = (p.RTTMs*3 + hb.RTTMs) / 4
			}
		}
	}
	e.bcast.SendTo(connID, e.envelope(protocol.SMsgHeartbeat, map[string]int64{
		"serverTime": now,
	}))
	// Push fresh telemetry to admin so RTT / active status stay live. Coalesced:
	// mark dirty and let the 1s flush ticker broadcast, rather than broadcasting
	// inline on every heartbeat (O(P^2) on the Run loop and floodable).
	e.telemetryDirty = true
}

// sendFullSync delivers an audience-scoped FULL_STATE_SYNC (§9). Mobile gets
// only the sanitized state flag; stage/admin additionally get board + reveal.
func (e *Engine) sendFullSync(connID string, role protocol.Role) {
	e.bcast.SendTo(connID, e.envelope(protocol.SMsgState, protocol.StateData{State: e.state}))
	// Solo Karaoke's active flag is the one signal EVERY role (including
	// mobile) may see — a bare boolean, zero track metadata (§4A) — so a
	// reconnecting client of any role resyncs to the idle-screen gate.
	e.bcast.SendTo(connID, e.envelope(smsgSoloKaraokeActive, soloKaraokeActiveData{Active: e.soloKaraoke.active}))
	// Mode-lock status: every (re)connecting admin tab must reflect the
	// current owner immediately, not just after the next flip — multiple
	// admin tabs can be open on /admin at once (solo_karaoke.go design note).
	if role == protocol.RoleAdmin {
		e.bcast.SendTo(connID, e.envelope(smsgModeLock, modeLockData{Owner: e.modeLockOwner()}))
	}
	if e.soloKaraoke.active && e.soloKaraoke.currentTrack != nil {
		if role == protocol.RoleAdmin || role == protocol.RoleStage {
			e.bcast.SendTo(connID, e.envelope(smsgSoloKaraokeNowPlaying, e.soloKaraokeNowPlayingData()))
		}
		// A stage (re)connecting mid-song (plausible over a multi-hour
		// event) must re-register playback with Spotify and resume at the
		// last known position — otherwise the device is silently
		// re-registered with no replay. This is a real gap for Solo
		// Karaoke specifically (trivia's own sendFullSync never re-issues
		// SMsgAudio either, but a trivia round is bounded by the buzzer/
		// reveal clock and the admin can just re-select the cell; Solo
		// Karaoke has no such recovery path).
		if role == protocol.RoleStage {
			posMs := e.soloKaraoke.pausedAtMs
			if !e.soloKaraoke.paused {
				posMs = nowMs() - e.soloKaraoke.trackStartMs
			}
			e.bcast.SendTo(connID, e.envelope(protocol.SMsgAudio, protocol.AudioData{
				Action: "play", TrackURI: e.soloKaraoke.currentTrack.SpotifyURI, PositionMs: posMs,
			}))
			if e.soloKaraoke.paused {
				e.bcast.SendTo(connID, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
			}
			// Re-send the current track's lyrics too — otherwise a
			// reconnecting stage replays audio but shows a blank lyrics view
			// until the next track change, since the "state" frame above
			// (LOBBY/GAME_OVER, pinned there for the duration of Solo
			// Karaoke) causes the stage's existing state handler to clear
			// its lyrics on every reconnect. soloKaraokeFetchAndSendLyrics
			// serves instantly from the singleflight cache in the common
			// case (lyrics were already fetched when the track started).
			e.soloKaraokeFetchAndSendLyrics()
		}
	}
	// Reconnect mid-reveal: send the CURRENT masked frame to ALL roles (mobile
	// included) so a reconnecting phone resyncs to exactly what the stage shows.
	// currentMask reflects only rc.*Revealed, so this can never leak ahead.
	if (e.rc.active || e.rc.finalizing) && e.curTrack != nil {
		e.bcast.SendTo(connID, e.envelope(smsgMaskedReveal, e.rc.currentMask()))
	}
	// Echo current reveal-timing knob values + telemetry to the admin control
	// room so its sliders and connections panel reflect server truth on
	// (re)connect (not just after the next heartbeat/ticker).
	if role == protocol.RoleAdmin {
		e.bcast.SendTo(connID, e.envelope(smsgAdminRevealCfg, e.revealCfgData()))
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgTelemetry, e.telemetryData()))
		e.bcast.SendTo(connID, e.envelope(smsgCheatReport, e.cheatReportData()))
		e.bcast.SendTo(connID, e.envelope(smsgVolumeStatus, e.volumeStatusData()))
	}
	// Scoreboard goes to everyone (handles + scores only, §4A-safe) so mobile
	// players see standings on connect too.
	e.bcast.SendTo(connID, e.envelope(protocol.SMsgScoreboard, e.scoreboardData()))
	// Daily Double performer status is sanitized (handle/ID only) — safe for a
	// reconnecting client of ANY role so its UI resyncs to "who's up" rather
	// than missing the original broadcast entirely.
	if e.state == protocol.StateDailyDouble {
		if pid, performing := e.dailyDoubleStatus(); pid != "" {
			if p := e.reg.players[pid]; p != nil {
				e.bcast.SendTo(connID, e.envelope(smsgDailyDoublePerformer, dailyDoublePerformerData{
					PlayerID: p.ID, Handle: p.Handle, Performing: performing,
				}))
			}
		}
	}
	// Picker status is sanitized (handle + hasPicked only) — safe for a
	// reconnecting client of ANY role, mirroring the Daily Double performer
	// resync above. Covers both the normal karaoke AND-gate window and the
	// simpler post-Daily-Double BOARD-state picking window. The picker's own
	// reconnecting connection also gets the board resent so a dropped
	// connection doesn't lose its ability to pick.
	if e.cellPicker != "" && (e.state == protocol.StateKaraoke || e.state == protocol.StateBoard) {
		e.bcast.SendTo(connID, e.envelope(smsgPickerStatus, e.pickerStatusData()))
		if p := e.reg.playerForConn(connID); p != nil && p.ID == e.cellPicker {
			e.bcast.SendTo(connID, e.envelope(smsgPickableBoard, boardData(e.board)))
		}
	}
	if protocol.TrustedReveal(role) {
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgBoard, boardData(e.board)))
		if e.curTrack != nil {
			e.bcast.SendTo(connID, e.trackStartEnvelope())
		}
		// A stage/admin (re)connecting during karaoke needs the round winner so
		// its banner is correct rather than defaulting to the scoreboard leader.
		if e.state == protocol.StateKaraoke {
			e.bcast.SendTo(connID, e.envelope(smsgRoundWinner, map[string]string{"handle": e.roundWinner}))
		}
	}
	// If Spotify OAuth already happened, tell a freshly-connected stage to
	// initialize the Web Playback SDK. We send an empty-token signal: the stage
	// fetches the actual (live, refreshed) token from /api/spotify/token. This
	// fixes the case where the stage connects AFTER the admin clicked Connect.
	if role == protocol.RoleStage && e.spotifyAuthed {
		e.bcast.SendTo(connID, e.envelope(smsgSpotifyToken, map[string]string{"token": ""}))
	}
}

// ---------------------------------------------------------------------------
// Cell selection + playback start (§3.3, §5, §7, §9)
// ---------------------------------------------------------------------------

func (e *Engine) onAdminSelect(connID string, env protocol.ClientEnvelope) {
	var d protocol.AdminSelectData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badSelect", err.Error())
		return
	}
	e.selectCell(d.Row, d.Col)
}

// selectCell picks a random unplayed track from the cell pool and starts the
// round (§7), UNLESS this selection is the cell's armed Daily Double visit —
// in which case no track plays at all; the Daily Double offer flow begins
// instead and the cell's pool is left untouched for a possible future pick.
// The cell stays live until its pool is exhausted (§7 persistent cells). Only
// valid from BOARD/KARAOKE/TRANSITION/etc. (not mid-round).
func (e *Engine) selectCell(row, col int) {
	if e.state == protocol.StateRoundActive || e.state == protocol.StateLocked ||
		e.state == protocol.StateAdjudicate || e.state == protocol.StateDailyDouble {
		// A round (or a Daily Double offer/pick/performance) is live; selection
		// is queued by the admin only between rounds.
		e.sendErrorAll("busy", "round in progress")
		return
	}
	// A new selection is starting — any player-picker state left over from
	// the karaoke/board phase that just ended is now stale, whether this
	// call is an admin override pre-empting a pending pick (admin's pick
	// always wins, immediately) or the auto-continuation using the exact
	// pick it already captured by value before calling in. Clearing here
	// means a stale pendingPick can never misfire against a later karaoke
	// phase, and broadcastPickerStatus below clears any "waiting on X"
	// UI still showing on other clients.
	if e.cellPicker != "" || e.pendingPick != nil {
		e.cellPicker = ""
		e.pendingPick = nil
		e.karaokeReady = false
		e.broadcastPickerStatus()
	}
	cell := cellAt(e.board, row, col)
	if cell == nil {
		e.sendErrorAll("badCell", "no such cell")
		return
	}
	if cell.Exhausted() {
		e.sendErrorAll("exhausted", "cell pool exhausted")
		return
	}
	key := cellKey(row, col)
	e.cellVisits[key]++
	if cell.DailyDouble && cell.DDTriggerVisit > 0 && e.cellVisits[key] == cell.DDTriggerVisit {
		e.beginDailyDoubleOffer(cell)
		return
	}
	track := pickTrack(cell, e.rng)
	if track == nil {
		e.sendErrorAll("exhausted", "cell pool exhausted")
		return
	}
	e.startTrack(cell, track)
}

// cellKey is the cellVisits map key for a 1-indexed (row, col) coordinate.
func cellKey(row, col int) string {
	return fmt.Sprintf("%d:%d", row, col)
}

// assignDailyDoubles clears every cell's Daily Double flag and randomly
// re-assigns e.board.DailyDoubleCount of them (§7 sidenote), balanced across
// categories: shuffle categories, give one Daily Double per category per
// pass, reshuffling each pass, until the configured count is placed or no
// eligible cell (≥2 tracks) remains anywhere. Degrades gracefully to fewer
// than requested rather than relaxing the ≥2-tracks/different-category rule.
// Called once per Start Game — never persisted per-cell.
func (e *Engine) assignDailyDoubles() {
	e.cellVisits = map[string]int{}
	if e.board == nil {
		return
	}
	catCells := map[string][]*Cell{}
	for _, row := range e.board.Cells {
		for _, c := range row {
			if c == nil {
				continue
			}
			c.DailyDouble = false
			c.DDTriggerVisit = 0
			if c.TracksLeft() >= 2 {
				catCells[c.Category] = append(catCells[c.Category], c)
			}
		}
	}
	n := e.board.DailyDoubleCount
	if n <= 0 || len(catCells) == 0 {
		return
	}
	categories := make([]string, 0, len(catCells))
	for cat := range catCells {
		categories = append(categories, cat)
	}

	placed := 0
	for placed < n {
		e.rng.Shuffle(len(categories), func(i, j int) { categories[i], categories[j] = categories[j], categories[i] })
		progressed := false
		for _, cat := range categories {
			if placed >= n {
				break
			}
			var elig []*Cell
			for _, c := range catCells[cat] {
				if !c.DailyDouble {
					elig = append(elig, c)
				}
			}
			if len(elig) == 0 {
				continue
			}
			pick := elig[e.rng.Intn(len(elig))]
			pick.DailyDouble = true
			pick.DDTriggerVisit = 2
			placed++
			progressed = true
		}
		if !progressed {
			break // no eligible cell left anywhere; degrade to fewer than requested
		}
	}
}

// startTrack arms a new buzz round: resets per-track player flags, bumps the
// nonce, sends trackStart to the STAGE only (it carries lengths, fine to show),
// the sanitized state to all, and issues the audio Play to the stage device.
func (e *Engine) startTrack(cell *Cell, track *Track) {
	e.curCell = cell
	e.curTrack = track
	e.curRow = cell.Row
	e.trackStartMs = nowMs()
	e.pausedAtMs = 0
	e.buzzWinner = ""
	e.roundWinner = "" // no winner yet this round
	e.partial = pendingPartial{}
	// Fresh round: any buzz window from a prior round is dead. A stale AfterFunc
	// is already neutralized by the roundKey compare in resolveBuzzWindow, but
	// clearing here is belt-and-suspenders (and the new roundKey below rearms).
	e.buzzWindowOpen = false
	e.buzzContenders = nil
	e.roundKey = fmt.Sprintf("%s:r%dc%d:%s:%d", e.cfg.SessionID, cell.Row, cell.Col, track.ID, e.trackStartMs)
	// eng7-1 (QA sweep 7): refreshCurDropPoint's async result is guarded by
	// comparing e.roundKey when it lands, so it must be called AFTER roundKey
	// is minted for THIS track, not before.
	e.refreshCurDropPoint()

	// New track => everyone may guess again (§3.4 one guess per track).
	for _, p := range e.reg.players {
		p.GuessedThisTrack = false
	}

	// Warm the lyric cache now so karaoke has no load time on the stage.
	e.prefetchLyrics(track.SpotifyURI, track.Artist, track.Song, int(track.DurationMs/1000))

	e.transitionTo(protocol.StateRoundActive)

	// trackStart carries point ceilings + answer LENGTHS for the stage timer.
	// The letter reveal is now server-authoritative: startReveal arms the clock
	// and streams masked frames to BOTH stage and mobile (see reveal.go). We no
	// longer send the full answer text to the stage during ROUND_ACTIVE — the
	// mask stream is the only reveal source, so a hostile client cannot read
	// ahead from the stage's memory either.
	e.bcast.Broadcast(protocol.RoleStage, e.trackStartEnvelope())
	// Clear any lingering lyrics from the previous track immediately, so the
	// old karaoke lines don't bleed 1-2s into the new song. Lyrics for THIS
	// track (if any) are fetched later at enterKaraoke; until then the stage
	// must show none.
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgLyrics, protocol.LyricsData{Lines: []protocol.LyricLine{}}))
	e.startReveal()
	e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())

	// Route playback to the stage's virtual device (§6, §9).
	e.loadAndPlayTrack(track, 0)
}

// loadAndPlayTrack is the playback primitive extracted out of startTrack: it
// routes a track to the stage's Spotify virtual device (§6, §9) and tells
// the stage to start local playback at positionMs. Pure extraction — startTrack's
// own round-bookkeeping (nonce bump, roundKey, GuessedThisTrack reset,
// transitionTo, lyric prefetch) is untouched and still happens around this
// call exactly as before. Shared with Solo Karaoke's soloKaraokePlayAt
// (solo_karaoke.go), which needs the identical primitive without any of
// trivia's round bookkeeping.
//
// Returns audio.Play's error (still logged here either way) so a caller
// that has somewhere to surface it can (Solo Karaoke does — see
// soloKaraokePlayAt/onAdminSoloKaraokeSeek, s10eng-07: a failed Play used to
// leave the session silently "active" on a track that never actually
// started, with no TrackEnded ever arriving to auto-advance away from it).
// Trivia's own call site deliberately keeps discarding it — unchanged
// behavior, not a regression this fix is scoped to touch.
func (e *Engine) loadAndPlayTrack(track *Track, positionMs int64) error {
	err := e.audio.Play(context.Background(), track.SpotifyURI, positionMs)
	if err != nil {
		log.Printf("[engine] audio.Play failed: %v", err)
	}
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{
		Action: "play", TrackURI: track.SpotifyURI, PositionMs: positionMs,
	}))
	return err
}

func (e *Engine) trackStartEnvelope() protocol.ServerEnvelope {
	// Defensive: never deref a nil track (callers should guard, but a crash
	// here takes down the whole server and every connection with it).
	artistLen, songLen := 0, 0
	if e.curTrack != nil {
		artistLen, songLen = len(e.curTrack.Artist), len(e.curTrack.Song)
	}
	// The LIVE pool, not the row's: a mid-round resume/reconnect must not hand
	// the stage back points a partial already took away.
	maxP, baseP := e.roundPool()
	return e.envelope(protocol.SMsgTrackStart, protocol.TrackStartData{
		MaxPoints:  maxP,
		BasePoints: baseP,
		StartTime:  e.trackStartMs,
		ArtistLen:  artistLen,
		SongLen:    songLen,
	})
}

// ---------------------------------------------------------------------------
// Buzz (§3.4, §4B, §4C, §4D, §9)
// ---------------------------------------------------------------------------

func (e *Engine) onBuzz(connID string, env protocol.ClientEnvelope, arrivalMs int64) {
	if e.state != protocol.StateRoundActive {
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		return
	}
	// Reveal lockout gate: the answer is essentially shown, so buzzing is closed.
	if e.revealLocked {
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		return
	}
	// §4D: drop stale-nonce buzzes (replay protection).
	if !e.gate.Validate(env.Nonce) {
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		return
	}
	p := e.reg.playerForConn(connID)
	if p == nil || p.Banned {
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		return
	}
	// §3.4 one guess per track: a player locked out / already-guessed can't win.
	if p.GuessedThisTrack {
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		return
	}

	// §4B: order by the server-stamped arrival time ALONE — it is the only
	// unforgeable ordering input. We gather every eligible buzz that lands within
	// a short collection window and pick the earliest arrival; command-enqueue
	// order does not decide the winner. The atomic BuzzLock is still taken for the
	// chosen winner so the cross-instance single-winner guarantee holds even
	// across a distributed Redis backend.
	//
	// NOTE (QA sweep 3, s3-fe): we deliberately do NOT apply §4C RTT latency
	// compensation. RTT was sourced from the client heartbeat (hb.RTTMs), and in a
	// zero-trust model a hostile mobile forges a high RTT to earn an earlier
	// effective time (EffectiveBuzzTime subtracts RTT/2), winning every contested
	// buzz. QUIC's own SmoothedRTT is not exposed by webtransport-go, and any
	// echo-based measurement is still gameable by a pong-delaying client, so no
	// server-trustable RTT exists. On a same-LAN venue real RTT spread is tiny, so
	// arrival-only ordering is both fair enough and unforgeable.
	c := buzzContender{playerID: p.ID, connID: connID, arrivalMs: arrivalMs}

	if e.buzzWindowOpen {
		// A window is already collecting: append (deduping by playerID, keeping the
		// earliest effective time). Do NOT send a result yet — resolution decides.
		e.addContender(c)
		return
	}

	// Open a new collection window for this round.
	e.buzzWindowOpen = true
	e.buzzContenders = []buzzContender{c}
	if e.buzzWindowMs <= 0 {
		// Synchronous path (tests / back-compat): resolve the 1-contender window
		// immediately — identical to the old serial first-buzz-wins behavior.
		e.resolveBuzzWindow(e.roundKey)
		return
	}
	// Async path: schedule resolution after the window elapses. Capture roundKey
	// so a late timer firing into a new round is ignored.
	rk := e.roundKey
	time.AfterFunc(time.Duration(e.buzzWindowMs)*time.Millisecond, func() {
		e.submit(func() { e.resolveBuzzWindow(rk) })
	})
}

// addContender appends a contender to the open window, deduping by playerID: a
// double-buzz from the same player keeps only their EARLIEST effective time
// (and its arrival) rather than double-counting.
func (e *Engine) addContender(c buzzContender) {
	for i := range e.buzzContenders {
		if e.buzzContenders[i].playerID == c.playerID {
			if c.arrivalMs < e.buzzContenders[i].arrivalMs {
				e.buzzContenders[i] = c
			}
			return
		}
	}
	e.buzzContenders = append(e.buzzContenders, c)
}

// resolveBuzzWindow closes the collection window and awards the buzz to the
// contender with the earliest server arrival time (ties broken RANDOMLY). It
// re-validates eligibility, takes the atomic BuzzLock for the
// winner (preserving the cross-instance single-winner guarantee), runs the
// winner path, and tells every other contender they lost. roundKey is compared
// against the live round so a stale timer from a superseded round is a no-op.
func (e *Engine) resolveBuzzWindow(roundKey string) {
	if e.state != protocol.StateRoundActive || e.roundKey != roundKey || !e.buzzWindowOpen {
		return // stale / superseded / already resolved
	}

	// Order contenders by server arrival time — the sole, unforgeable ordering
	// input (§4B; see onBuzz note on why §4C RTT compensation is intentionally
	// not applied).
	//
	// TIE-BREAK IS RANDOM (QA sweep 4, s4-ws-new-x1). arrivalMs has 1ms
	// granularity, so at a conference two phones on the same Wi-Fi tie constantly
	// — this is the common case, not a corner case. The old tie-break was
	// `a.playerID < b.playerID`, which handed every one of those ties to the
	// lexicographically smallest player ID for the whole game. Player IDs are
	// client-influenced, so a player who could steer their ID low would win every
	// tie in every round: a systematic, farmable advantage from a field that is
	// not supposed to affect ordering at all.
	//
	// Pre-shuffle, then sort STABLY on arrivalMs alone, so the shuffle survives as
	// the intra-tie order. Stability is what makes e.rng the DEFINED and seedable
	// source of the tie-break; plain sort.Slice is an unstable pdqsort that would
	// throw the shuffle away and substitute its own pivot-dependent permutation.
	// (That permutation is not ID-biased, so swapping it in would not resurrect
	// the bug this replaced — it would just make fairness an accident of the sort
	// implementation instead of a property we can seed and test. Do not swap it.)
	// e.rng is the engine's injectable source (Deps.Rand) and this runs on the
	// engine's single goroutine, so it needs no lock and tests can seed it.
	order := make([]buzzContender, len(e.buzzContenders))
	copy(order, e.buzzContenders)
	e.rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	sort.SliceStable(order, func(i, j int) bool { return order[i].arrivalMs < order[j].arrivalMs })

	// Pick the best still-eligible contender. A contender that went offline /
	// banned / already-guessed since buzzing is skipped in favor of the next.
	var winner *buzzContender
	winIdx := -1
	for i := range order {
		p := e.reg.players[order[i].playerID]
		if p == nil || p.Banned || p.GuessedThisTrack || !e.reg.online(p.ID) {
			continue
		}
		winner = &order[i]
		winIdx = i
		break
	}
	if winner == nil {
		// Nobody eligible remains; close the window, round stays active.
		e.buzzWindowOpen = false
		e.buzzContenders = nil
		return
	}

	p := e.reg.players[winner.playerID]

	// Atomic cross-instance single-winner guarantee (§4 hard rule).
	won, err := e.lock.TryAcquire(context.Background(), e.roundKey, p.ID)
	if err != nil || !won {
		// Another instance took the round: this whole window loses.
		for i := range order {
			for _, cid := range e.reg.connIDs(order[i].playerID) {
				e.bcast.SendTo(cid, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
			}
		}
		e.buzzWindowOpen = false
		e.buzzContenders = nil
		return
	}

	// Winner path (factored out of the old inline block; behavior identical).
	e.awardBuzzWinner(p, winner.arrivalMs)

	// Every OTHER contender loses.
	for i := range order {
		if i == winIdx {
			continue
		}
		for _, cid := range e.reg.connIDs(order[i].playerID) {
			e.bcast.SendTo(cid, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		}
	}

	e.buzzWindowOpen = false
	e.buzzContenders = nil
}

// awardBuzzWinner runs the winning-buzz side effects: pause audio via the direct
// stage path (~20ms, §9), transition LOCKED -> ADJUDICATE, tell the winner they
// won + lock out mobile, hand the admin the evaluation context, and log the
// buzz with its arrival time (§3.4, §3.5, §4B, §8C).
func (e *Engine) awardBuzzWinner(p *Player, arrivalMs int64) {
	e.buzzWinner = p.ID
	e.pausedAtMs = nowMs() - e.trackStartMs
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{
		Action: "pause", PositionMs: e.pausedAtMs,
	}))

	e.transitionTo(protocol.StateLocked)

	// Tell the winner they won; everyone else loses. Mobile also gets lockout.
	for _, cid := range e.reg.connIDs(p.ID) {
		e.bcast.SendTo(cid, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: true}))
	}
	e.bcast.Broadcast(protocol.RoleMobile, e.envelope(protocol.SMsgLockout, protocol.LockoutData{ByHandle: p.Handle}))

	// Hand control to the admin with the full evaluation context (§3.5, §8C).
	e.transitionTo(protocol.StateAdjudicate)
	e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())
	_ = e.repo.LogEvent(context.Background(), e.cfg.SessionID, "buzz", map[string]any{
		"playerID": p.ID, "handle": p.Handle, "arrivalMs": arrivalMs,
	})
}

// ---------------------------------------------------------------------------
// Grade (§3.6, §7, §9). A second concurrent grade is naturally ignored: by the
// time it runs, e.state has already left ADJUDICATE.
// ---------------------------------------------------------------------------

func (e *Engine) onGrade(connID string, env protocol.ClientEnvelope) {
	if e.state != protocol.StateAdjudicate {
		// First-packet-wins: the round already moved on (§9).
		return
	}
	var d protocol.AdminGradeData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badGrade", err.Error())
		return
	}
	winner := e.reg.players[e.buzzWinner]
	if winner == nil {
		e.endRound()
		return
	}
	winner.GuessedThisTrack = true
	elapsed := e.pausedAtMs

	switch d.Verdict {
	case protocol.VerdictCorrect:
		e.gradeCorrect(winner, elapsed)
	case protocol.VerdictPartial:
		e.gradePartial(winner, elapsed, d.PartialKind)
	case protocol.VerdictIncorrect:
		e.gradeIncorrect(winner)
	default:
		e.sendError(connID, "badVerdict", string(d.Verdict))
		return
	}
}

// gradeCorrect awards decayed points; winner picks the next cell; buzzer stays
// disabled; the cell's track is consumed and we head to karaoke (§3.6, §7).
func (e *Engine) gradeCorrect(winner *Player, elapsed int64) {
	// elapsed is relative to trackStartMs, which a partial re-anchors.
	maxP, baseP := e.roundPool()
	pts := currentPointsFromPool(maxP, baseP, elapsed)
	e.award(winner, pts)
	e.cellPicker = winner.ID
	e.roundWinner = winner.Handle // the real winner for the karaoke banner
	e.buzzWinner = ""
	e.lock.Release(context.Background(), e.roundKey)
	e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())

	e.consumeTrack() // consume this track from the pool (§7)

	// Note: a flagged cell's normal (non-trigger) picks still buzz/grade like
	// any other cell — Daily Double activation is decided entirely in
	// selectCell (on the exact armed visit), never here.
	e.enterKaraoke()
}

// gradePartial awards 50, keeps the remaining pool alive, resumes audio, and
// re-enables the buzzer for everyone EXCEPT players who already guessed this
// track (§3.6, §7).
func (e *Engine) gradePartial(winner *Player, elapsed int64, kind string) {
	secondPartial := e.partial.active
	awarded, remaining := PartialAward(e.curRow, elapsed)
	if secondPartial {
		// Two partials should not exceed the pool; second partial claims the
		// rest of the pool minus its own 50. Clamp at the leftover.
		awarded = PartialPoints
		remaining = e.partial.remaining - PartialPoints
		if remaining < 0 {
			remaining = 0
		}
	}
	e.award(winner, awarded)
	e.partial = pendingPartial{active: true, remaining: remaining}
	for _, cid := range e.reg.connIDs(winner.ID) {
		e.bcast.SendTo(cid, e.envelope(smsgGradeResult, map[string]string{"verdict": "partial"}))
	}

	if secondPartial {
		// Two partials exhaust guessing; clear admin evaluation and enter karaoke.
		e.buzzWinner = ""
		e.lock.Release(context.Background(), e.roundKey)
		e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())
		e.consumeTrack()
		e.enterKaraoke()
		return
	}

	// Resume audio inline instead of resumeAudio(): we re-anchor the timer 5001ms
	// in the past (below) rather than at pausedAtMs, and send only one trackStart.
	_ = e.audio.Resume(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{
		Action: "resume", PositionMs: e.pausedAtMs,
	}))

	// Re-anchor timer with the reduced pool. Start 5001ms in the past so the
	// hold period is already elapsed and decay begins immediately.
	e.trackStartMs = nowMs() - 5001
	// e.partial is already set above, so trackStartEnvelope's roundPool()
	// carries the reduced pool.
	e.bcast.Broadcast(protocol.RoleStage, e.trackStartEnvelope())

	// Force the graded field fully revealed on BOTH surfaces via the mask
	// (server-authoritative reveal), and keep the stage-only cosmetic
	// partialReveal signal. Sent AFTER trackStart so the stage's new-track
	// logic clears its flags first, then we re-set the revealed field.
	if kind != "" {
		e.revealFieldFully(kind)
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgPartialReveal, map[string]string{"field": kind}))
	}

	e.buzzWinner = ""
	e.lock.Release(context.Background(), e.roundKey)
	e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())
	e.transitionTo(protocol.StateRoundActive)
	e.reenableEligible()
}

// gradeIncorrect locks the guesser out for this track permanently, resumes
// audio from where it paused, and re-enables remaining eligible players (§3.6).
func (e *Engine) gradeIncorrect(winner *Player) {
	winner.GuessedThisTrack = true // permanent lockout for this track
	for _, cid := range e.reg.connIDs(winner.ID) {
		e.bcast.SendTo(cid, e.envelope(smsgGradeResult, map[string]string{"verdict": "incorrect"}))
	}
	e.resumeAudio()
	e.buzzWinner = ""
	e.lock.Release(context.Background(), e.roundKey)
	e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())
	e.transitionTo(protocol.StateRoundActive)
	e.reenableEligible()
}

// reenableEligible re-arms the buzzer for every mobile player who has not yet
// guessed this track. Players who guessed get nothing (button stays locked).
func (e *Engine) reenableEligible() {
	for _, p := range e.reg.mobilePlayers() {
		if p.GuessedThisTrack || p.Banned {
			continue
		}
		for _, cid := range e.reg.connIDs(p.ID) {
			// A fresh buzzResult{won:false} clears the lockout overlay client-side.
			e.bcast.SendTo(cid, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		}
	}
}

// resumeAudio resumes Spotify playback from the paused position (§3.6).
func (e *Engine) resumeAudio() {
	_ = e.audio.Resume(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{
		Action: "resume", PositionMs: e.pausedAtMs,
	}))
	// Re-anchor the timer so decay continues from where it paused (§5).
	e.trackStartMs = nowMs() - e.pausedAtMs
	// Send re-anchored trackStart so the stage timer unfreezes correctly.
	e.bcast.Broadcast(protocol.RoleStage, e.trackStartEnvelope())
}

// award credits points and broadcasts the updated scoreboard (trusted only).
// Also updates lastScorer for GAMEPLAY points (buzz win, partial, Daily Double
// payout) — the Daily Double contestant pointer. Manual admin.award
// adjustments bypass this helper entirely (see onAdminAward), so they never
// affect who gets picked for the next Daily Double.
func (e *Engine) award(p *Player, pts int) {
	p.Score += pts
	if pts > 0 {
		e.lastScorer = p.ID
	}
	_ = e.repo.SaveScore(context.Background(), e.cfg.SessionID, p.ID, p.Handle, p.Score)
	e.broadcastScoreboard()
	e.broadcastTelemetry()
}

// ---------------------------------------------------------------------------
// Karaoke + skip voting (§3.7, §3.8)
// ---------------------------------------------------------------------------

// adminReveal is the admin force-revealing the answer mid-round. It enters
// karaoke (shows answer + lyrics, disables guessing) without awarding points.
func (e *Engine) adminReveal() {
	e.consumeTrack()
	e.buzzWinner = ""
	e.lock.Release(context.Background(), e.roundKey)
	e.enterKaraoke()
}

// enterKaraoke resumes audio, reveals the answer + lyrics to STAGE only, and
// opens the skip vote. The active pool is snapshotted at this instant (§3.8).
func (e *Engine) enterKaraoke() {
	e.resumeAudio()
	e.transitionTo(protocol.StateKaraoke)

	// Complete the streamed letter reveal to BOTH surfaces (the answer is now
	// public on the projector), then send the trusted full reveal + lyrics to
	// stage/admin only (album art + exact-cased text the karaoke view needs;
	// mobile still never receives SMsgReveal).
	e.finalizeReveal()
	e.revealTo(protocol.RoleStage)
	e.revealTo(protocol.RoleAdmin)
	// Tell the stage who actually won this round (or nobody) so its banner is
	// correct — never falling back to the scoreboard leader.
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgRoundWinner, map[string]string{"handle": e.roundWinner}))
	e.fetchAndSendLyrics()

	// §3.8 active pool: players who joined before voting started and are online.
	e.votingPool = map[string]bool{}
	e.votes = map[string]bool{}
	for _, p := range e.reg.mobilePlayers() {
		if p.Active && !p.Banned && e.reg.online(p.ID) {
			e.votingPool[p.ID] = true
		}
	}
	e.broadcastVoteState()
	// The current track was just consumed (Played=true); re-broadcast the board
	// so the admin's grid shows the updated tracks-left / exhausted state
	// immediately, rather than only after returning to BOARD — otherwise they
	// can click a now-empty cell and hit "cell pool exhausted".
	e.broadcastBoard()

	// Fresh karaoke phase: e.cellPicker was just set (gradeCorrect, right
	// before this call) to whoever won the round. Open their picking window:
	// tell everyone who's picking, and send that one player's phone the
	// board so they can choose. pendingPick/karaokeReady start this phase
	// clear (selectCell already reset them when THIS round started).
	e.broadcastPickerStatus()
	e.sendPickableBoard()
}

// fetchAndSendLyrics delivers synced lyrics to the stage, sharing the SAME
// fetch the prefetch (at startTrack) already kicked off — via a singleflight
// cache entry. If that fetch is already done (the common case, since prefetch
// ran seconds ago) it serves instantly with no spinner. If it's still in
// flight, it WAITS on it off the Run goroutine rather than flashing loading +
// starting a duplicate fetch (the old bug that produced the ~1-in-5 spinner).
// "loading" is only shown if the shared fetch hasn't finished within a short
// grace — i.e. essentially only when the reveal is hit almost immediately.
func (e *Engine) fetchAndSendLyrics() {
	if e.lyrics == nil || e.curTrack == nil {
		return
	}
	spotifyURI := e.curTrack.SpotifyURI
	artist, song := e.curTrack.Artist, e.curTrack.Song
	durSec := int(e.curTrack.DurationMs / 1000)
	rk := e.roundKey
	ent := e.lyricFetch(spotifyURI, artist, song, durSec) // shares/starts the singleflight fetch

	send := func() {
		if e.roundKey != rk {
			return // round moved on; drop stale lyrics
		}
		if ent.err != nil || len(ent.lines) == 0 {
			e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgLyricsStatus, map[string]string{"status": "none"}))
			return
		}
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgLyricsStatus, map[string]string{"status": "ready"}))
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgLyrics, protocol.LyricsData{Lines: ent.lines}))
	}

	select {
	case <-ent.done:
		// Already fetched (prefetch finished): serve immediately, no spinner.
		send()
	default:
		// Still in flight: wait for the shared fetch, then serve. Only show a
		// spinner if it doesn't land within a short grace window.
		go func() {
			select {
			case <-ent.done:
			case <-time.After(400 * time.Millisecond):
				e.submit(func() {
					if e.roundKey == rk {
						e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgLyricsStatus, map[string]string{"status": "loading"}))
					}
				})
				<-ent.done
			}
			e.submit(send)
		}()
	}
}

// prefetchLyrics warms the lyric cache for a track off the Run loop (called at
// startTrack) so karaoke has the lines in hand. It just starts/join the shared
// fetch; the result is consumed later by fetchAndSendLyrics.
func (e *Engine) prefetchLyrics(spotifyURI, artist, song string, durSec int) {
	if e.lyrics == nil {
		return
	}
	e.lyricFetch(spotifyURI, artist, song, durSec)
}

// lyricEntry is a singleflight cache slot: one fetch shared by the prefetch and
// the karaoke send. done is closed when the fetch completes.
type lyricEntry struct {
	done  chan struct{}
	lines []protocol.LyricLine
	err   error
}

// lyricFetch returns the cache entry for a track, starting the fetch once if
// it isn't already in flight/complete. DB-first: if lyricsStore has a
// resolved entry for spotifyURI (fetched or manually entered during board
// building), it's used directly with no LRCLIB call at all. Otherwise falls
// back to a live lyrics.Fetch, exactly as before the cache existed. A failed
// live fetch is evicted so a later call retries (a transient LRCLIB error
// shouldn't poison the track forever).
func (e *Engine) lyricFetch(spotifyURI, artist, song string, durSec int) *lyricEntry {
	key := lyricKey(spotifyURI, artist, song, durSec)
	e.lyricMu.Lock()
	if e.lyricCache == nil {
		e.lyricCache = map[string]*lyricEntry{}
	}
	if ent, ok := e.lyricCache[key]; ok {
		e.lyricMu.Unlock()
		return ent
	}
	ent := &lyricEntry{done: make(chan struct{})}
	e.lyricCache[key] = ent
	e.lyricMu.Unlock()

	go func() {
		if e.lyricsStore != nil && spotifyURI != "" {
			if text, ok, err := e.lyricsStore.GetResolvedLyrics(context.Background(), spotifyURI); err == nil && ok {
				ent.lines = lyrics.ParseLRC(text)
				close(ent.done)
				return
			}
		}
		lines, err := e.lyrics.Fetch(context.Background(), artist, song, durSec)
		ent.lines, ent.err = lines, err
		close(ent.done)
		if err != nil {
			// Evict on error so the next reveal/prefetch retries.
			e.lyricMu.Lock()
			delete(e.lyricCache, key)
			e.lyricMu.Unlock()
		}
	}()
	return ent
}

func lyricKey(spotifyURI, artist, song string, durSec int) string {
	if spotifyURI != "" {
		return spotifyURI
	}
	return fmt.Sprintf("%s|%s|%d", artist, song, durSec)
}

func (e *Engine) onVote(connID string, env protocol.ClientEnvelope) {
	if e.state != protocol.StateKaraoke {
		return
	}
	if !e.gate.Validate(env.Nonce) { // §4D
		return
	}
	p := e.reg.playerForConn(connID)
	if p == nil {
		return
	}
	// Late joiners during the voting phase don't count (§3.8): only pool members.
	if !e.votingPool[p.ID] {
		return
	}
	e.votes[p.ID] = true
	e.broadcastVoteState()
	e.evaluateSkipVotes()
}

// needVotes computes the dynamic threshold: votes must STRICTLY EXCEED pct% of
// the active pool, or EQUAL 100% when the slider is maxed (§3.8).
func (e *Engine) needVotes() int {
	active := 0
	for id := range e.votingPool {
		if e.reg.online(id) {
			active++
		}
	}
	pct := e.session.SkipThresholdPct
	if pct >= 100 {
		return active // 100% => everyone must vote (equal, not exceed)
	}
	// strictly exceed pct% => floor(active*pct/100) + 1
	need := (active*pct)/100 + 1
	if need > active {
		need = active
	}
	if need < 1 && active > 0 {
		need = 1
	}
	return need
}

func (e *Engine) haveVotes() int {
	n := 0
	for id := range e.votes {
		if e.votingPool[id] && e.reg.online(id) {
			n++
		}
	}
	return n
}

func (e *Engine) broadcastVoteState() {
	have, need := e.haveVotes(), e.needVotes()
	for _, p := range e.reg.mobilePlayers() {
		voted := e.votes[p.ID]
		for _, cid := range e.reg.connIDs(p.ID) {
			e.bcast.SendTo(cid, e.envelope(protocol.SMsgVoteState, protocol.VoteStateData{
				Have: have, Need: need, Voted: voted,
			}))
		}
	}
}

// evaluateSkipVotes triggers the transition once the dynamic threshold is met
// (§3.8/§3.9). Recalculates on every vote AND on disconnect.
func (e *Engine) evaluateSkipVotes() {
	if e.state != protocol.StateKaraoke {
		return
	}
	active := 0
	for id := range e.votingPool {
		if e.reg.online(id) {
			active++
		}
	}
	if active == 0 {
		return
	}
	if e.haveVotes() >= e.needVotes() {
		e.karaokeConditionMet()
	}
}

// beginTransition stops audio, shows the countdown, then either starts the
// picker's already-chosen cell directly (the normal case, once both AND-gate
// halves are satisfied — see karaokeConditionMet) or falls back to BOARD and
// waits for the admin, exactly like before this feature existed.
func (e *Engine) beginTransition() {
	_ = e.audio.Pause(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
	e.transitionTo(protocol.StateTransition)

	// Countdown then return to board. Done off-loop with a timer that re-enters
	// the loop via submit so state stays single-threaded. The delay is read here
	// on the loop goroutine (not inside the timer goroutine) to stay race-free.
	delay := TransitionDelay
	go func() {
		time.Sleep(delay)
		e.submit(func() {
			if e.state == protocol.StateTransition {
				e.clearReveal()
				e.curTrack = nil
				e.curCell = nil
				pending := e.pendingPick
				e.pendingPick = nil
				e.transitionTo(protocol.StateBoard)
				e.broadcastBoard()
				if pending != nil {
					// Re-validate: the cell could in principle have become
					// invalid in the 3s window (not reachable under normal
					// play today, but cheap to guard rather than assume).
					if cell := cellAt(e.board, pending.Row, pending.Col); cell != nil && !cell.Exhausted() {
						e.selectCell(pending.Row, pending.Col)
						return
					}
				}
				// No valid pending pick (the fallback path — nobody picked,
				// shouldn't normally happen given the AND-gate — or the
				// picked cell turned out invalid). Clear the stale picker
				// status so clients stop showing "waiting on X"; it's the
				// admin's turn now, same as before this feature existed.
				e.cellPicker = ""
				e.karaokeReady = false
				e.broadcastPickerStatus()
			}
		})
	}()
}

// ---------------------------------------------------------------------------
// Daily Double (§7). Lifecycle: selectCell's armed visit -> beginDailyDoubleOffer
// (contestant asked accept/decline) -> onDailyDoubleDecision (decline reroll via
// declineDailyDouble, or accept draws candidates + pushes the contestant-only
// offer) -> onDailyDoubleChoose -> startDailyDoublePerformance (track plays,
// crowd rates) -> finishDailyDouble (average -> payout -> straight back to
// BOARD; no karaoke afterward — the sing-along already happened live).
// ---------------------------------------------------------------------------

// beginDailyDoubleOffer resolves the contestant (lastScorer, falling back to a
// random connected mobile player if nobody has scored yet) and asks them to
// accept/decline. If nobody is eligible at all, the Daily Double is cancelled
// outright for this cell (reverts to normal) rather than stalling the game.
func (e *Engine) beginDailyDoubleOffer(cell *Cell) {
	contestantID := e.lastScorer
	// dd-eng-3 (QA sweep 6): mirror the fallback path's !p.Banned filter here.
	// registry.online() is purely connection-count based (player.go) and has
	// nothing to do with Banned — onAdminKick sets p.Banned=true but never
	// force-disconnects the transport (it just sends an error frame the
	// client is trusted to honor), so a banned player who ignores it stays
	// online() == true. Without this check, a recently-banned lastScorer would
	// skip straight past the fallback and be offered the Daily Double anyway.
	lastScorer := e.reg.players[contestantID]
	if contestantID == "" || lastScorer == nil || lastScorer.Banned || !e.reg.online(contestantID) {
		var online []*Player
		for _, p := range e.reg.mobilePlayers() {
			if !p.Banned && e.reg.online(p.ID) {
				online = append(online, p)
			}
		}
		if len(online) == 0 {
			cell.DailyDouble = false
			cell.DDTriggerVisit = 0
			e.sendErrorAll("noDailyDoubleContestant", "no eligible player online; skipping this Daily Double")
			return
		}
		contestantID = online[e.rng.Intn(len(online))].ID
	}

	e.curCell = cell
	e.ddOffer = &dailyDoubleOffer{cellRow: cell.Row, cellCol: cell.Col, contestantID: contestantID}
	e.transitionTo(protocol.StateDailyDouble)
	e.broadcastDailyDoublePerformer()
}

// dailyDoubleStatus reports the current Daily Double contestant (offer/pick
// sub-phase) or performer (a track is playing), and whether the performance
// has started. Returns pid == "" when there is nothing to report.
func (e *Engine) dailyDoubleStatus() (pid string, performing bool) {
	switch {
	case e.ddOffer != nil:
		return e.ddOffer.contestantID, false
	case e.curTrack != nil && e.cellPicker != "":
		return e.cellPicker, true
	default:
		return "", false
	}
}

// broadcastDailyDoublePerformer sends the sanitized (handle/ID only, no track
// data) Daily Double status to every role — safe for mobile (§4A) since it
// carries nothing but who's up and whether the performance has started.
func (e *Engine) broadcastDailyDoublePerformer() {
	pid, performing := e.dailyDoubleStatus()
	if pid == "" {
		return
	}
	p := e.reg.players[pid]
	if p == nil {
		return
	}
	e.bcast.BroadcastAll(e.envelope(smsgDailyDoublePerformer, dailyDoublePerformerData{
		PlayerID: p.ID, Handle: p.Handle, Performing: performing,
	}))
}

// pickerStatusData builds the sanitized "who's picking" payload — handle +
// whether they've picked, no board/track data.
func (e *Engine) pickerStatusData() pickerStatusData {
	var id, handle string
	if p := e.reg.players[e.cellPicker]; p != nil {
		id, handle = p.ID, p.Handle
	}
	return pickerStatusData{PickerID: id, PickerHandle: handle, HasPicked: e.pendingPick != nil}
}

// broadcastPickerStatus sends the current picker status to every role. Safe
// for mobile (§4A) — a handle + a bool, nothing else. Called whenever the
// picker, or whether they've picked, changes (enterKaraoke, a Daily Double
// resolving straight to BOARD, onPlayerSelectCell, and selectCell's reset).
func (e *Engine) broadcastPickerStatus() {
	e.bcast.BroadcastAll(e.envelope(smsgPickerStatus, e.pickerStatusData()))
}

// sendPickableBoard delivers the board (category/points/exhausted — already
// §4A-safe, no track metadata) to ONLY the designated picker's own
// connection(s), so their phone can render a tappable grid. No other mobile
// client ever receives this message.
func (e *Engine) sendPickableBoard() {
	if e.cellPicker == "" {
		return
	}
	env := e.envelope(smsgPickableBoard, boardData(e.board))
	for _, cid := range e.reg.connIDs(e.cellPicker) {
		e.bcast.SendTo(cid, env)
	}
}

// karaokeConditionMet is called when the "ready to leave karaoke" half of
// the AND-gate becomes true (skip vote threshold reached, or the track
// ended naturally — see evaluateSkipVotes / onStagePlayerState). If the
// picker has already chosen (e.pendingPick != nil), both halves are now
// satisfied — transition immediately. Otherwise just record this half and
// wait for onPlayerSelectCell to catch up: whichever of the two resolves
// second is what actually fires beginTransition.
func (e *Engine) karaokeConditionMet() {
	if e.pendingPick != nil {
		e.beginTransition()
		return
	}
	e.karaokeReady = true
}

// onPlayerSelectCell lets the designated picker (e.cellPicker) choose the
// next board cell from their own phone. Only the connection bound to that
// playerID may act (§4D nonce-gated, mirroring onDailyDoubleChoose's
// single-connection scoping). Legal in two states:
//   - StateBoard: right after a Daily Double, which transitions straight to
//     BOARD with no karaoke/vote phase — pick immediately, exactly like
//     admin.select already does, since there's no AND-gate to wait on.
//   - StateKaraoke: the normal post-round flow — record the pick and only
//     actually start it once karaokeConditionMet's other half has also
//     fired (see karaokeConditionMet).
func (e *Engine) onPlayerSelectCell(connID string, env protocol.ClientEnvelope) {
	if e.state != protocol.StateKaraoke && e.state != protocol.StateBoard {
		return
	}
	if !e.gate.Validate(env.Nonce) { // §4D
		return
	}
	p := e.reg.playerForConn(connID)
	// s8eng-01: mirror onBuzz's p.Banned check — onAdminKick now also clears
	// e.cellPicker for the kicked player (see onAdminKick), but a merely-
	// banned-not-kicked edge case or an ordering race must not let a banned
	// player's hostile client keep steering the board via a stale connID.
	if p == nil || p.Banned || e.cellPicker == "" || p.ID != e.cellPicker {
		return
	}
	var d playerSelectCellData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badSelect", err.Error())
		return
	}
	cell := cellAt(e.board, d.Row, d.Col)
	if cell == nil || cell.Exhausted() {
		e.sendError(connID, "badCell", "no such cell or exhausted")
		return
	}
	if e.state == protocol.StateBoard {
		e.selectCell(d.Row, d.Col)
		return
	}
	// StateKaraoke: part of the AND-gate. A double-submit (two taps before
	// the first's state change is visible) is silently ignored — harmless,
	// not an error, since the first tap already recorded a valid pick.
	if e.pendingPick != nil {
		return
	}
	e.pendingPick = &cellCoord{Row: d.Row, Col: d.Col}
	e.broadcastPickerStatus()
	if e.karaokeReady {
		e.beginTransition()
	}
}

// onDailyDoubleDecision handles the contestant's accept/decline. Only the
// connection bound to the offered contestant may respond (§4D nonce-gated).
func (e *Engine) onDailyDoubleDecision(connID string, env protocol.ClientEnvelope) {
	if e.state != protocol.StateDailyDouble || e.ddOffer == nil {
		return
	}
	if !e.gate.Validate(env.Nonce) {
		return
	}
	p := e.reg.playerForConn(connID)
	if p == nil || p.ID != e.ddOffer.contestantID {
		return
	}
	var d dailyDoubleDecisionData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return
	}
	if !d.Accept {
		e.declineDailyDouble()
		return
	}

	// dd-eng-2 (QA sweep 6): make accept idempotent. Accepting does NOT call
	// transitionTo (state stays DAILY_DOUBLE), so the nonce gate is never
	// bumped between accept and the eventual choose — the client's nonce stays
	// Validate()-true for a repeat send. A double-tap, a client-side retry
	// after a dropped ack, or a hostile replay of the identical
	// {accept:true} envelope must not draw ANOTHER batch from the
	// no-replacement DDBucket and silently discard the first, already-sent
	// batch. If candidates were already drawn for this offer, just resend
	// them instead of drawing again.
	if len(e.ddOffer.candidates) > 0 {
		choices := make([]dailyDoubleSongChoice, len(e.ddOffer.candidates))
		for i, t := range e.ddOffer.candidates {
			choices[i] = dailyDoubleSongChoice{ID: t.ID, Title: t.Song, Artist: t.Artist}
		}
		for _, cid := range e.reg.connIDs(p.ID) {
			e.bcast.SendTo(cid, e.envelope(smsgDailyDoubleOffer, dailyDoubleOfferData{Songs: choices}))
		}
		return
	}

	n := 5
	if len(e.board.DDBucket) < n {
		n = len(e.board.DDBucket)
	}
	if n == 0 {
		// Nothing in the bucket to offer; don't stall the game on an empty pool.
		e.declineDailyDouble()
		return
	}
	e.rng.Shuffle(len(e.board.DDBucket), func(i, j int) {
		e.board.DDBucket[i], e.board.DDBucket[j] = e.board.DDBucket[j], e.board.DDBucket[i]
	})
	picked := e.board.DDBucket[:n]
	e.board.DDBucket = e.board.DDBucket[n:] // no-replacement draw across the whole game
	e.ddOffer.candidates = picked

	choices := make([]dailyDoubleSongChoice, len(picked))
	for i, t := range picked {
		choices[i] = dailyDoubleSongChoice{ID: t.ID, Title: t.Song, Artist: t.Artist}
	}
	// CONTRACT-QUESTION §4A exception (see const block above): contestant's own
	// connection(s) only, just-in-time, never persisted client-side.
	for _, cid := range e.reg.connIDs(p.ID) {
		e.bcast.SendTo(cid, e.envelope(smsgDailyDoubleOffer, dailyDoubleOfferData{Songs: choices}))
	}
}

// onDailyDoubleChoose handles the contestant picking one of their offered songs.
func (e *Engine) onDailyDoubleChoose(connID string, env protocol.ClientEnvelope) {
	if e.state != protocol.StateDailyDouble || e.ddOffer == nil {
		return
	}
	if !e.gate.Validate(env.Nonce) {
		return
	}
	p := e.reg.playerForConn(connID)
	if p == nil || p.ID != e.ddOffer.contestantID {
		return
	}
	var d dailyDoubleChooseData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return
	}
	var chosen *Track
	for _, t := range e.ddOffer.candidates {
		if t.ID == d.TrackID {
			chosen = t
			break
		}
	}
	if chosen == nil {
		return
	}
	e.startDailyDoublePerformance(p, chosen)
}

// startDailyDoublePerformance begins playback of the chosen song. Unlike a
// normal round, nothing is masked: full metadata + lyrics go straight to
// stage/admin (trusted roles), and there is no buzz/guess mechanic at all —
// the crowd rates 1-5 stars instead (§7 sidenote).
func (e *Engine) startDailyDoublePerformance(performer *Player, track *Track) {
	cell := cellAt(e.board, e.ddOffer.cellRow, e.ddOffer.cellCol)
	if cell == nil {
		// dd-eng-4 (QA sweep 6): the offer's cellRow/cellCol no longer resolve on
		// the CURRENT board — the only way this happens is an admin swapping the
		// board (ReloadBoard, which has no state check at all) while this offer
		// was still outstanding. Proceeding here used to leave e.curRow at
		// whatever a PRIOR round set it to, so finishDailyDouble() would price
		// the payout off the WRONG row's point ceiling. Treat a vanished cell as
		// a hard cancel — abort exactly like a contestant decline — rather than
		// perform against a phantom cell.
		log.Printf("[engine] startDailyDoublePerformance: cell (%d,%d) no longer resolves on the current board; aborting Daily Double", e.ddOffer.cellRow, e.ddOffer.cellCol)
		e.declineDailyDouble()
		return
	}
	e.clearReveal() // defensive: no masked-reveal clock is used for a performance
	e.curCell = cell
	e.curTrack = track
	e.curRow = cell.Row
	e.trackStartMs = nowMs()
	e.cellPicker = performer.ID
	// dd-eng-1 (QA sweep 6): mint a FRESH roundKey, mirroring startTrack's own
	// pattern, before e.ddOffer is cleared below (it still holds cellRow/
	// cellCol here). Without this, e.roundKey stays pinned at whatever the
	// PRIOR normal round's startTrack() set, so a still-in-flight
	// fetchAndSendLyrics closure from that round (which captured `rk :=
	// e.roundKey`) evaluates its staleness guard as false during this live
	// Daily Double and can broadcast the wrong track's lyrics over it.
	e.roundKey = fmt.Sprintf("%s:dd:r%dc%d:%s:%d", e.cfg.SessionID, e.ddOffer.cellRow, e.ddOffer.cellCol, track.ID, e.trackStartMs)
	// eng7-1 (QA sweep 7): same ordering requirement as startTrack -- must
	// come after roundKey is minted so refreshCurDropPoint's staleness guard
	// checks against THIS performance's round, not the one that just ended.
	e.refreshCurDropPoint()
	e.ddOffer = nil
	// trackStartEnvelope derives its pool from roundPool(), same as startTrack
	// — reset here for the same reason startTrack does: a stale partial from
	// the round immediately before this Daily Double must not leak a wrong
	// pool into its trackStart.
	e.partial = pendingPartial{}

	e.revealTo(protocol.RoleStage)
	e.revealTo(protocol.RoleAdmin)
	e.bcast.Broadcast(protocol.RoleStage, e.trackStartEnvelope())
	e.fetchAndSendLyrics()
	e.broadcastDailyDoublePerformer()

	e.ratingPool = map[string]bool{}
	e.ratings = map[string]int{}
	for _, p := range e.reg.mobilePlayers() {
		if p.ID == performer.ID || p.Banned || !e.reg.online(p.ID) {
			continue
		}
		e.ratingPool[p.ID] = true
	}

	if err := e.audio.Play(context.Background(), track.SpotifyURI, 0); err != nil {
		log.Printf("[engine] audio.Play failed: %v", err)
	}
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{
		Action: "play", TrackURI: track.SpotifyURI, PositionMs: 0,
	}))

	// Deliberately NOT short-circuiting to finishDailyDouble when ratingPool is
	// empty: the performance should still play out (naturally ending via
	// onStagePlayerState, or an admin Force End Round) rather than the song
	// getting paused a moment after it starts. finishDailyDouble already
	// floors at 1★ if literally nobody ends up rating.
}

// declineDailyDouble handles a decline (contestant or admin force-skip): the
// original cell permanently reverts to normal, and a new random cell with ≥1
// unplayed track (looser than the initial ≥2/different-category rule, which
// only governs the initial balanced assignment) is armed to fire on its very
// next selection. Cancels outright (no replacement) if none exists.
func (e *Engine) declineDailyDouble() {
	if e.state != protocol.StateDailyDouble || e.ddOffer == nil {
		return
	}
	declined := cellAt(e.board, e.ddOffer.cellRow, e.ddOffer.cellCol)
	if declined != nil {
		declined.DailyDouble = false
		declined.DDTriggerVisit = 0
	}
	e.ddOffer = nil

	var candidates []*Cell
	if e.board != nil {
		for _, row := range e.board.Cells {
			for _, c := range row {
				if c == nil || c == declined || c.DailyDouble {
					continue
				}
				if c.TracksLeft() >= 1 {
					candidates = append(candidates, c)
				}
			}
		}
	}
	if len(candidates) > 0 {
		next := candidates[e.rng.Intn(len(candidates))]
		next.DailyDouble = true
		next.DDTriggerVisit = e.cellVisits[cellKey(next.Row, next.Col)] + 1
	}
	// else: no eligible cell anywhere — cancel this Daily Double for the game.

	e.curCell = nil
	e.transitionTo(protocol.StateBoard)
	e.broadcastBoard()
}

func (e *Engine) onRate(connID string, env protocol.ClientEnvelope) {
	if e.state != protocol.StateDailyDouble {
		return
	}
	if !e.gate.Validate(env.Nonce) {
		return
	}
	p := e.reg.playerForConn(connID)
	if p == nil || !e.ratingPool[p.ID] {
		return
	}
	var d protocol.RateData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return
	}
	if d.Stars < 1 || d.Stars > 5 {
		return
	}
	e.ratings[p.ID] = d.Stars

	// Once every eligible rater has voted, average + apply the payout (§7).
	e.checkDailyDoubleComplete()
}

// checkDailyDoubleComplete finishes the daily double EARLY once every
// eligible rater has voted (§7) — it never cuts a performance short just
// because the pool is/became empty; an empty pool means the song plays to its
// natural end (or an admin Force End Round) with a graceful 1★-floor payout
// (see finishDailyDouble), not an instant pause the moment it starts.
// Guarded on curTrack != nil so a disconnect/kick during the offer/song-pick
// sub-phase (state already DAILY_DOUBLE, but no performance started yet and
// ratingPool not yet built) is a safe no-op rather than a premature finish.
//
// Routes through fadeOutAndFinishDailyDouble (not finishDailyDouble
// directly) so the "all ratings are in" completion fades gracefully too,
// not just the admin.endDailyDouble button — the abrupt instant stop this
// used to do was the actual complaint being fixed here.
func (e *Engine) checkDailyDoubleComplete() {
	if e.state != protocol.StateDailyDouble || e.curTrack == nil {
		return
	}
	if len(e.ratingPool) > 0 && len(e.ratings) >= len(e.ratingPool) {
		e.fadeOutAndFinishDailyDouble()
	}
}

// finishDailyDouble averages whatever ratings exist (floored at 1★/mult 1.0 if
// nobody rated — the performer still performed), applies DailyDoubleMultiplier
// to the FULL row value (there is no antecedent correct-guess score to bonus
// on top of in this design), awards it, broadcasts the sanitized result, and
// returns straight to BOARD — no karaoke phase follows.
//
// Deliberately computes from the raw MaxPointsForRow(e.curRow), NOT through
// roundPool() — this is not a site the sweep-4 accessor refactor missed. The
// crowd-rating bonus is a separate performance-multiplier mechanic
// (scoring.go's locked doc comment: DailyDoubleMultiplier "maps an average
// crowd star rating to the performance bonus multiplier applied to the max
// track value"), and it is never broadcast as a projected decaying pool, so
// there is no display-vs-award divergence to guard against here.
func (e *Engine) finishDailyDouble() {
	performer := e.reg.players[e.cellPicker]
	if performer != nil {
		avg := 1.0
		if len(e.ratings) > 0 {
			sum := 0
			for _, s := range e.ratings {
				sum += s
			}
			avg = float64(sum) / float64(len(e.ratings))
		}
		mult := DailyDoubleMultiplier(avg)
		pts := int(float64(MaxPointsForRow(e.curRow)) * mult)
		e.award(performer, pts)
		_ = e.repo.LogEvent(context.Background(), e.cfg.SessionID, "dailyDouble", map[string]any{
			"playerID": performer.ID, "avgStars": avg, "points": pts,
		})
		e.bcast.BroadcastAll(e.envelope(smsgDailyDoubleResult, dailyDoubleResultData{AvgStars: avg, Points: pts}))
	}
	_ = e.audio.Pause(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
	e.clearReveal()
	e.curTrack = nil
	e.curCell = nil
	e.ratingPool = nil
	e.ratings = nil
	e.transitionTo(protocol.StateBoard)
	e.broadcastBoard()

	// A Daily Double resolves straight to BOARD with no karaoke/vote phase,
	// so the performer's picking window (same "DD winner becomes next
	// picker" rule as a normal correct guess, since both go through the
	// shared award() path) opens here directly via onPlayerSelectCell's
	// StateBoard branch, instead of waiting on the AND-gate used in
	// enterKaraoke. e.cellPicker already holds the performer's ID (set in
	// startDailyDoublePerformance); only clear it if that player somehow no
	// longer resolves (e.g. a kick mid-performance).
	if performer == nil {
		e.cellPicker = ""
	}
	e.broadcastPickerStatus()
	e.sendPickableBoard()
}

// ---------------------------------------------------------------------------
// Admin overrides (§3.10, §9)
// ---------------------------------------------------------------------------

func (e *Engine) onAdminPlayback(connID string, env protocol.ClientEnvelope) {
	var d protocol.AdminPlaybackData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badPlayback", err.Error())
		return
	}
	// No track loaded (between rounds) => nothing to pause/resume. Guard here so
	// a stray Play/Pause click can't nil-deref e.curTrack in resumeAudio.
	if e.curTrack == nil {
		return
	}
	switch d.Action {
	case "pause":
		e.pausedAtMs = nowMs() - e.trackStartMs
		_ = e.audio.Pause(context.Background())
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
	case "resume", "play":
		e.resumeAudio()
	}
}

// CONTRACT-QUESTION: cmsgAdminSeek lets the control room scrubber jump
// playback to an arbitrary position. protocol.go's AdminPlaybackData is
// {action string} only, with no position field, so a new local type carries
// it. Defined here per the fixed-contract process; discuss with the project
// owner for promotion into protocol.go on a version bump.
const cmsgAdminSeek protocol.ClientMsgType = "admin.seek"

type adminSeekData struct {
	PositionMs int64 `json:"positionMs"`
}

// onAdminSeek jumps Spotify playback to an admin-chosen position, for the
// control-room scrubber.
func (e *Engine) onAdminSeek(connID string, env protocol.ClientEnvelope) {
	var d adminSeekData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badSeek", err.Error())
		return
	}
	e.seekTo(d.PositionMs)
}

// seekTo issues the actual playback jump: commands Spotify to positionMs and
// broadcasts the same to stage. Shared by onAdminSeek and
// onAdminJumpToDropPoint. Deliberately does NOT touch e.trackStartMs/
// e.pausedAtMs: unlike resumeAudio's re-anchor (which only ever restores a
// position identical to where the round's own decay clock already was, since
// nothing moves playback except the round itself), a seek can jump the song
// anywhere — including past the 60s decay window scoring.go caps at. Letting
// decay follow the seek would let an admin cueing up a specific part of a
// song accidentally zero out the bonus for every player. Decay stays on its
// own clock; only the song's playback position moves.
//
// eng7-2 (QA sweep 7): positionMs is CLAMPED (not rejected) to
// [0, e.curTrack.DurationMs], centralized here so both callers (onAdminSeek,
// onAdminJumpToDropPoint) get the bound without duplicating it. Clamping
// rather than erroring out is deliberate for this path: a scrub/seek target
// is ephemeral and already approximate — onAdminJumpToDropPoint's own
// pre-existing `target < 0 -> 0` clamp is the precedent this generalizes to
// also cover the upper bound. Contrast with onAdminSetDropPoint below, which
// REJECTS out-of-range input instead, because that value is persisted.
// DurationMs<=0 (unknown length) skips the upper clamp since there's nothing
// trustworthy to bound against.
func (e *Engine) seekTo(positionMs int64) {
	if e.curTrack == nil {
		return
	}
	if positionMs < 0 {
		positionMs = 0
	}
	if e.curTrack.DurationMs > 0 && positionMs > e.curTrack.DurationMs {
		positionMs = e.curTrack.DurationMs
	}
	_ = e.audio.Play(context.Background(), e.curTrack.SpotifyURI, positionMs)
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{
		Action: "play", TrackURI: e.curTrack.SpotifyURI, PositionMs: positionMs,
	}))
}

// dropPointStoreTimeout bounds the drop-point store calls that now resolve
// off the Run loop (eng7-1, QA sweep 7). The loop itself no longer waits on
// these at all; this is defense in depth so a hung DB connection can't leak
// the goroutine (and the connection) forever, not the fix for the blocking
// bug itself.
const dropPointStoreTimeout = 3 * time.Second

// refreshCurDropPoint caches e.curTrack's drop-point mark (nil = none known
// yet), so onAdminJumpToDropPoint and the admin position broadcast don't need
// a DB round-trip per call. Called whenever curTrack is set to a new track —
// AFTER e.roundKey has been minted for that track at both call sites
// (startTrack, startDailyDoublePerformance), so the staleness guard below
// compares against the round that's actually starting, not the one that just
// ended.
//
// eng7-1 (QA sweep 7): GetDropPoint used to run synchronously, inline on the
// Run loop, with an unbounded context.Background() — a slow store blocked
// EVERY other command (buzzes, admin actions, heartbeats) for the full call
// latency (confirmed empirically: a 2s-delay fake store blocked a trivial
// queued command for the full 2s). This now mirrors lyricFetch/
// fetchAndSendLyrics's shape exactly: the slow call runs in a goroutine off
// the loop, and the result is posted back via e.submit. The roundKey
// captured before the goroutine starts is re-checked when the result lands;
// if a newer round has started in the meantime, the stale result is dropped
// instead of clobbering whatever curDropPointMs the new round already has.
func (e *Engine) refreshCurDropPoint() {
	e.curDropPointMs = nil
	if e.dropPointStore == nil || e.curTrack == nil {
		return
	}
	spotifyURI := e.curTrack.SpotifyURI
	rk := e.roundKey
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dropPointStoreTimeout)
		defer cancel()
		ms, ok, err := e.dropPointStore.GetDropPoint(ctx, spotifyURI)
		if err != nil || !ok {
			return
		}
		e.submit(func() {
			if e.roundKey != rk {
				return // stale: a newer round started before this lookup landed
			}
			e.curDropPointMs = &ms
		})
	}()
}

// CONTRACT-QUESTION: cmsgAdminSetDropPoint/cmsgAdminJumpToDropPoint let the
// control room mark a song's "best part" position once (reusing the
// scrubber's live position) and jump back to it later. protocol.go has no
// field for this, so these are local types per the fixed-contract process;
// discuss with the project owner for promotion into protocol.go on a
// version bump.
const (
	cmsgAdminSetDropPoint    protocol.ClientMsgType = "admin.setDropPoint"
	cmsgAdminJumpToDropPoint protocol.ClientMsgType = "admin.jumpToDropPoint"
)

type adminSetDropPointData struct {
	PositionMs int64 `json:"positionMs"`
}

// dropPointSeekBufferMs is how far before the marked position "Jump to Drop"
// actually seeks — a short lead-in so the drop itself isn't missed. The
// user's own ask was "3, maybe 5" seconds; splitting the difference as a
// named, trivially tunable constant.
const dropPointSeekBufferMs = 4000

// onAdminSetDropPoint marks the CURRENTLY PLAYING track's drop point at the
// given position, always as a human ("manual") mark — this handler is only
// ever reachable from the control room's own "Mark Drop Point" button, never
// from any future automation (which would call SetDropPoint directly with a
// different source, bypassing this handler entirely).
//
// eng7-3 (QA sweep 7): all three ways this used to silently no-op (no store
// configured, no track playing, or the DB write itself failing) now call
// sendError with a distinct code, so the control room learns WHY "Mark Drop
// Point" didn't take instead of discovering it indirectly later (e.g. via
// eng7-4's "noDropPoint" on a subsequent Jump attempt).
//
// eng7-2 (QA sweep 7): positionMs is validated against the track's own
// DurationMs and REJECTED (not clamped) when out of range — unlike seekTo's
// ephemeral scrub position, this value is persisted to track_drop_point and
// can never be overwritten by a later automated write (ports.go's
// never-clobber-manual rule), so a bad value written here is sticky across
// every future placement of the song. The operator should see why their mark
// didn't take, not silently get an adjusted value instead.
//
// eng7-1 (QA sweep 7): the DB write (SetDropPoint) now runs off the Run loop
// in a goroutine, the same shape as refreshCurDropPoint. Unlike that read,
// the value to cache here is already known — it's the admin's own input —
// so e.curDropPointMs updates immediately (preserving the existing "Jump
// works right away, no round-trip wait" behavior) and only the persistence
// itself is async; a write failure is reported back via sendError once it
// lands, guarded by roundKey so a failure from a now-stale round doesn't
// surface against whatever track is playing by then.
func (e *Engine) onAdminSetDropPoint(connID string, env protocol.ClientEnvelope) {
	var d adminSetDropPointData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badSetDropPoint", err.Error())
		return
	}
	if e.dropPointStore == nil {
		e.sendError(connID, "dropPointDisabled", "drop-point marking is not configured on this server")
		return
	}
	if e.curTrack == nil {
		e.sendError(connID, "dropPointNoTrack", "no track is currently playing")
		return
	}
	if d.PositionMs < 0 || (e.curTrack.DurationMs > 0 && d.PositionMs > e.curTrack.DurationMs) {
		e.sendError(connID, "dropPointOutOfRange", fmt.Sprintf("positionMs %d outside track bounds [0,%d]", d.PositionMs, e.curTrack.DurationMs))
		return
	}

	spotifyURI := e.curTrack.SpotifyURI
	positionMs := d.PositionMs
	rk := e.roundKey
	e.curDropPointMs = &positionMs
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dropPointStoreTimeout)
		defer cancel()
		if err := e.dropPointStore.SetDropPoint(ctx, spotifyURI, positionMs, "manual"); err != nil {
			log.Printf("[engine] setDropPoint: %v", err)
			e.submit(func() {
				if e.roundKey != rk {
					return // round moved on; the save failure no longer applies to the current track
				}
				e.sendError(connID, "dropPointSaveFailed", "failed to save drop point")
			})
		}
	}()
}

// onAdminJumpToDropPoint seeks to the current track's marked drop point minus
// a short lead-in buffer, via the same seekTo path (and the same deliberate
// decay-independence) as the scrubber's manual seek.
//
// eng7-4 (QA sweep 7): the three distinct causes a jump can fail for (feature
// disabled, no track currently playing, or a track with genuinely no mark)
// used to all share one "noDropPoint" error code. Split into three so an
// operator troubleshooting on-site isn't stuck guessing which one it is.
func (e *Engine) onAdminJumpToDropPoint(connID string, _ protocol.ClientEnvelope) {
	if e.dropPointStore == nil {
		e.sendError(connID, "dropPointDisabled", "drop-point marking is not configured on this server")
		return
	}
	if e.curTrack == nil {
		e.sendError(connID, "dropPointNoTrack", "no track is currently playing")
		return
	}
	if e.curDropPointMs == nil {
		e.sendError(connID, "noDropPoint", "no drop point set for the current track")
		return
	}
	target := *e.curDropPointMs - dropPointSeekBufferMs
	if target < 0 {
		target = 0
	}
	e.seekTo(target)
}

// CONTRACT-QUESTION: cmsgAdminSetVolume/cmsgAdminEndDailyDouble/
// smsgVolumeStatus. protocol.go has no field for an operating volume level
// or a graceful Daily Double end, so these are local types per the fixed-
// contract process; discuss with the project owner for promotion into
// protocol.go on a version bump.
const (
	cmsgAdminSetVolume      protocol.ClientMsgType = "admin.setVolume"
	cmsgAdminEndDailyDouble protocol.ClientMsgType = "admin.endDailyDouble"
	smsgVolumeStatus        protocol.ServerMsgType = "volumeStatus"
)

type adminSetVolumeData struct {
	Percent int `json:"percent"`
}

type volumeStatusData struct {
	Percent int `json:"percent"`
}

// dailyDoubleFadeSteps/DailyDoubleFadeDuration control the graceful
// fade-out's granularity. DailyDoubleFadeDuration is a var (not const) so
// tests can shrink it, mirroring TransitionDelay's own override pattern.
const dailyDoubleFadeSteps = 6

var DailyDoubleFadeDuration = 3 * time.Second

func (e *Engine) volumeStatusData() volumeStatusData {
	return volumeStatusData{Percent: e.volumePct}
}

func (e *Engine) broadcastVolumeStatus() {
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgVolumeStatus, e.volumeStatusData()))
}

// onAdminSetVolume sets the operating playback volume, applied immediately
// to the live device so the control room's slider has an audible effect
// right away — independent of any Daily Double performance. This is also
// the level fadeOutAndFinishDailyDouble restores to once a fade completes.
func (e *Engine) onAdminSetVolume(connID string, env protocol.ClientEnvelope) {
	var d adminSetVolumeData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badSetVolume", err.Error())
		return
	}
	if d.Percent < 0 {
		d.Percent = 0
	} else if d.Percent > 100 {
		d.Percent = 100
	}
	e.volumePct = d.Percent
	e.broadcastVolumeStatus()
	// s8eng-05: route through the single serializing writer instead of an ad
	// hoc goroutine, so this can never race the fade's own SetVolume calls.
	e.sendVolume(d.Percent)
}

// sendVolume enqueues an outbound SetVolume call to the single writer
// goroutine runVolumeWriter owns (s8eng-05), instead of firing a bare,
// independently-scheduled goroutine straight at the live device per call
// site. Every call site (admin.setVolume's live-apply, the fade's stepped
// ramp, and the fade's restore) goes through here, so the device always
// applies them in the same order they were issued — closing the
// network-level reordering race between them. Never call e.audio.SetVolume
// directly from anywhere else.
func (e *Engine) sendVolume(pct int) {
	select {
	case e.volumeCmds <- pct:
	default:
		// Buffer full (64 pending volume changes implies something is
		// already badly wrong) — drop rather than block whichever goroutine
		// is calling this (possibly the Run loop itself); a later call will
		// still land and the device settles on the latest intent either way.
		log.Printf("[engine] sendVolume: volumeCmds buffer full, dropping percent=%d", pct)
	}
}

// runVolumeWriter is the single writer for every outbound AudioDevice.
// SetVolume call, started by Run alongside the main command loop and
// running for the engine's lifetime (s8eng-05). Draining one channel with
// one consumer gives strict FIFO ordering that matches enqueue order,
// regardless of which goroutine (admin.setVolume's handler, the fade's
// stepped-ramp goroutine, or the fade's restore) called sendVolume.
func (e *Engine) runVolumeWriter(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case pct := <-e.volumeCmds:
			_ = e.audio.SetVolume(context.Background(), pct)
		}
	}
}

// onAdminEndDailyDouble lets the control room end a Daily Double
// performance early with the same graceful fade the automatic all-rated
// completion uses, instead of the instant Force End Round/
// admin.skipDailyDouble kill switches. Legal only while a performance is
// actually in progress (curTrack set) — matches checkDailyDoubleComplete's
// own guard, and the admin UI only shows this button under the identical
// condition.
func (e *Engine) onAdminEndDailyDouble(connID string, _ protocol.ClientEnvelope) {
	if e.state != protocol.StateDailyDouble || e.curTrack == nil {
		e.sendError(connID, "noDailyDoublePerformance", "no Daily Double performance is currently in progress")
		return
	}
	e.fadeOutAndFinishDailyDouble()
}

// fadeOutAndFinishDailyDouble is the one shared path for ending a Daily
// Double performance gracefully — used by both checkDailyDoubleComplete's
// automatic all-rated completion and the admin.endDailyDouble button. Force
// End Round / admin.skipDailyDouble deliberately do NOT go through this —
// they stay instant, true emergency stops.
//
// Guaranteed volume restore: restoreTo is captured BEFORE the fade starts,
// and the defer that restores it is registered as the very first thing in
// the submitted continuation, before any other logic runs — so the device
// is restored to its configured operating level whether the continuation
// completes normally, bails out early (round already ended some other way
// while the fade was in flight), or finishDailyDouble's own body does
// anything unexpected. The volume can never get stuck faded down.
func (e *Engine) fadeOutAndFinishDailyDouble() {
	if e.state != protocol.StateDailyDouble || e.curTrack == nil {
		return
	}
	// s8eng-03: identity-check against the CURRENT e.roundKey, not a bare
	// bool — a fade already in flight for THIS exact performance is a
	// no-op (double-fire guard), but a stale marker left behind by an
	// EARLIER, now-ended performance never matches here, since e.roundKey
	// has already moved on to this (or no) performance.
	if e.ddFadingRoundKey != "" && e.ddFadingRoundKey == e.roundKey {
		return
	}
	rk := e.roundKey
	e.ddFadingRoundKey = rk
	restoreTo := e.volumePct

	go func() {
		stepDelay := DailyDoubleFadeDuration / dailyDoubleFadeSteps
		// s8eng-07: start at dailyDoubleFadeSteps (not -1) so the first
		// computed level equals restoreTo itself — the fade's first
		// SetVolume call reaffirms the current level before stepping down,
		// eliminating the audible notch an immediate drop to a
		// already-stepped-down level produced.
		for i := dailyDoubleFadeSteps; i >= 0; i-- {
			level := restoreTo * i / dailyDoubleFadeSteps
			e.sendVolume(level) // s8eng-05: through the single serializing writer
			time.Sleep(stepDelay)
		}
		e.submit(func() {
			defer func() {
				// s8eng-04: re-read the LIVE e.volumePct here instead of
				// reusing the restoreTo snapshot captured at fade-start, so
				// a mid-fade admin.setVolume naturally wins the restore —
				// the device and the admin UI (both driven by this same
				// value) can never desync.
				e.sendVolume(e.volumePct)
				// s8eng-03: only clear the marker if it still belongs to
				// THIS fade — a stale continuation must not clobber a
				// different, legitimately in-flight fade's marker (e.g. a
				// new performance that started its own fade while this
				// stale one was still sleeping).
				if e.ddFadingRoundKey == rk {
					e.ddFadingRoundKey = ""
				}
				e.broadcastVolumeStatus()
			}()
			// s8eng-03: a Force End Round/admin.skipDailyDouble may have
			// already finished THIS round, or an entirely new performance
			// may have started since (fresh e.roundKey) — either way,
			// e.roundKey no longer matches the performance this fade was
			// started for, so bail without finishing (or misfiring
			// against) whatever is live now. The defer above still
			// restores the volume regardless.
			if e.roundKey != rk || e.state != protocol.StateDailyDouble || e.curTrack == nil {
				return
			}
			e.finishDailyDouble()
		})
	}()
}

func (e *Engine) onAdminAward(connID string, env protocol.ClientEnvelope) {
	var d protocol.AdminAwardData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badAward", err.Error())
		return
	}
	if d.Delta < -1000 {
		d.Delta = -1000
	}
	if d.Delta > 1000 {
		d.Delta = 1000
	}
	if p := e.reg.players[d.PlayerID]; p != nil {
		p.Score += d.Delta
		if p.Score < 0 {
			p.Score = 0
		}
		_ = e.repo.SaveScore(context.Background(), e.cfg.SessionID, p.ID, p.Handle, p.Score)
		e.broadcastScoreboard()
	}
}

func (e *Engine) onAdminKick(connID string, env protocol.ClientEnvelope) {
	var d protocol.AdminKickData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badKick", err.Error())
		return
	}
	p := e.reg.players[d.PlayerID]
	if p == nil {
		return
	}
	if d.Ban {
		p.Banned = true
	}
	p.Active = false
	delete(e.votingPool, p.ID)
	delete(e.ratingPool, p.ID)
	delete(e.ratings, p.ID) // keep ratings in sync with pool (§3.8)
	for _, cid := range e.reg.connIDs(p.ID) {
		e.bcast.SendTo(cid, e.envelope(protocol.SMsgError, protocol.ErrorData{Code: "kicked", Message: "removed by admin"}))
	}
	if e.state == protocol.StateKaraoke {
		e.evaluateSkipVotes()
	}
	if e.state == protocol.StateDailyDouble {
		if e.ddOffer != nil && e.ddOffer.contestantID == p.ID {
			e.declineDailyDouble()
		} else {
			e.checkDailyDoubleComplete()
		}
	}
	// s8eng-01: mirror the DD-contestant-kick special case above — a kicked
	// cell-picker must not retain picking rights (onPlayerSelectCell's own
	// p.Banned guard closes the hostile-client angle, but e.cellPicker/
	// e.pendingPick must also be cleared so clients stop showing "waiting
	// on X" for a player who can never act again).
	if e.cellPicker == p.ID {
		e.cellPicker = ""
		e.pendingPick = nil
		e.karaokeReady = false
		e.broadcastPickerStatus()
	}
}

func (e *Engine) onAdminSetThresh(connID string, env protocol.ClientEnvelope) {
	var d protocol.AdminSetThreshData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badThresh", err.Error())
		return
	}
	if d.Percent < 50 {
		d.Percent = 50
	}
	if d.Percent > 100 {
		d.Percent = 100
	}
	e.session.SkipThresholdPct = d.Percent
	if e.state == protocol.StateKaraoke {
		e.broadcastVoteState()
		e.evaluateSkipVotes()
	}
}

// revealCfgData snapshots the current reveal-timing knobs for the admin echo.
func (e *Engine) revealCfgData() adminRevealCfgData {
	c := e.revealCfg
	return adminRevealCfgData{
		IntervalMs:    c.IntervalMs,
		Phase1Ms:      c.Phase1Ms,
		BlockMs:       c.BlockMs,
		EaseMs:        c.EaseMs,
		LockoutChars:  c.LockoutChars,
		AutoKaraokeMs: c.AutoKaraokeMs,
		GenreDelayMs:  c.GenreDelayMs,
		YearDelayMs:   c.YearDelayMs,
		Alternate:     c.Alternate,
	}
}

// onAdminSetRevealCfg updates the live reveal-timing knobs. The change is stored
// on the engine (NOT the in-flight rc); the next startTrack snapshots it, so it
// applies to the NEXT round. Echoes the clamped values back to all admins.
func (e *Engine) onAdminSetRevealCfg(connID string, env protocol.ClientEnvelope) {
	var d adminSetRevealCfgData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badRevealCfg", err.Error())
		return
	}
	cfg := e.revealCfg
	if d.IntervalMs != nil {
		cfg.IntervalMs = *d.IntervalMs
	}
	if d.Phase1Ms != nil {
		cfg.Phase1Ms = *d.Phase1Ms
	}
	if d.BlockMs != nil {
		cfg.BlockMs = *d.BlockMs
	}
	if d.EaseMs != nil {
		cfg.EaseMs = *d.EaseMs
	}
	if d.LockoutChars != nil {
		cfg.LockoutChars = *d.LockoutChars
	}
	if d.AutoKaraokeMs != nil {
		cfg.AutoKaraokeMs = *d.AutoKaraokeMs
	}
	if d.GenreDelayMs != nil {
		cfg.GenreDelayMs = *d.GenreDelayMs
	}
	if d.YearDelayMs != nil {
		cfg.YearDelayMs = *d.YearDelayMs
	}
	if d.Alternate != nil {
		cfg.Alternate = *d.Alternate
	}
	e.revealCfg = cfg.clamp()
	// Echo the applied values to every admin so all control-room tabs agree.
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgAdminRevealCfg, e.revealCfgData()))
}

// skipDailyDouble abandons/aborts whatever Daily Double sub-phase is
// currently live: if a performance is playing it finishes exactly like a
// natural song-end (average whatever ratings are in); if the contestant is
// still deciding/picking, it's treated as a decline (reroll to a new cell).
// No-op outside StateDailyDouble. Shared by endRound (force-end) and the
// dedicated admin.skipDailyDouble action (dd-eng-5, QA sweep 6) — before this
// helper existed, skipDailyDouble called declineDailyDouble() UNCONDITIONALLY,
// which no-ops once e.ddOffer is nil (i.e. once a performance has actually
// started: startDailyDoublePerformance clears it), so the dedicated Skip
// button silently did nothing during a live performance despite this
// function's own doc comment claiming parity with endRound.
func (e *Engine) skipDailyDouble() {
	if e.state != protocol.StateDailyDouble {
		return
	}
	switch {
	case e.curTrack != nil:
		e.finishDailyDouble()
	case e.ddOffer != nil:
		e.declineDailyDouble()
	}
}

// endRound force-ends the current round and returns to the board (§3.10). The
// SAME button also finishes/abandons a Daily Double via skipDailyDouble — the
// dedicated admin.skipDailyDouble action shares that exact logic.
func (e *Engine) endRound() {
	if e.state == protocol.StateDailyDouble {
		e.skipDailyDouble()
		return
	}
	e.consumeTrack()
	e.lock.Release(context.Background(), e.roundKey)
	_ = e.audio.Pause(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
	e.clearReveal()
	e.curTrack = nil
	e.curCell = nil
	e.buzzWinner = ""
	e.buzzWindowOpen = false
	e.buzzContenders = nil
	e.partial = pendingPartial{}
	e.transitionTo(protocol.StateBoard)
	e.broadcastBoard()
}

// ---------------------------------------------------------------------------
// Stage device callbacks (§6)
// ---------------------------------------------------------------------------

func (e *Engine) onStageDeviceReady(connID string, env protocol.ClientEnvelope) {
	var d protocol.StageDeviceReadyData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return
	}
	e.audio.SetDevice(d.SpotifyDeviceID)
}

func (e *Engine) onStagePlayerState(connID string, env protocol.ClientEnvelope) {
	var d protocol.StagePlayerStateData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return
	}
	e.broadcastPlaybackPosition(d)
	if !d.TrackEnded {
		return
	}
	// Solo Karaoke owns the deck exclusively while active (mode-lock), so
	// trivia's own TrackEnded handling below must never run concurrently
	// with it — route and return instead of falling into the trivia switch
	// below with the wrong curTrack.
	if e.soloKaraoke.active {
		e.soloKaraokeAdvance()
		return
	}
	switch e.state {
	case protocol.StateKaraoke:
		e.karaokeConditionMet()
	case protocol.StateRoundActive:
		e.endRound()
	case protocol.StateDailyDouble:
		if e.curTrack != nil {
			e.finishDailyDouble()
		}
	}
}

// CONTRACT-QUESTION: smsgPlaybackPosition relays the live playback position
// stage already reports (protocol.go's StagePlayerStateData, used today only
// for TrackEnded) to the admin control-room scrubber. Admin-only; carries no
// track metadata beyond duration (already known server-side per track, same
// field used for LRCLIB duration-matching), so it adds nothing a trusted
// admin role can't already see elsewhere.
const smsgPlaybackPosition protocol.ServerMsgType = "playbackPosition"

type playbackPositionData struct {
	PositionMs   int64 `json:"positionMs"`
	DurationMs   int64 `json:"durationMs"`
	Paused       bool  `json:"paused"`
	HasDropPoint bool  `json:"hasDropPoint"`
	DropPointMs  int64 `json:"dropPointMs,omitempty"`
}

// broadcastPlaybackPosition relays stage's live position/pause state to the
// admin scrubber, plus the current track's cached drop-point mark (if any)
// so the UI can render a marker on the bar and enable/disable "Jump to
// Drop" without a separate round-trip. No-ops with no track loaded (nothing
// to show).
func (e *Engine) broadcastPlaybackPosition(d protocol.StagePlayerStateData) {
	if e.soloKaraoke.active {
		if e.soloKaraoke.currentTrack == nil {
			return
		}
		e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgPlaybackPosition, playbackPositionData{
			PositionMs: d.PositionMs,
			DurationMs: e.soloKaraoke.currentTrack.DurationMs,
			Paused:     d.Paused,
		}))
		return
	}
	if e.curTrack == nil {
		return
	}
	pd := playbackPositionData{
		PositionMs: d.PositionMs,
		DurationMs: e.curTrack.DurationMs,
		Paused:     d.Paused,
	}
	if e.curDropPointMs != nil {
		pd.HasDropPoint = true
		pd.DropPointMs = *e.curDropPointMs
	}
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgPlaybackPosition, pd))
}

// ---------------------------------------------------------------------------
// Reveal / scoreboard / board helpers — sanitization lives here (§4A).
// ---------------------------------------------------------------------------

// revealTo sends the trusted reveal payload to a single role. NEVER mobile.
func (e *Engine) revealTo(role protocol.Role) {
	if !protocol.TrustedReveal(role) || e.curTrack == nil {
		return
	}
	e.bcast.Broadcast(role, e.envelope(protocol.SMsgReveal, protocol.RevealData{
		Artist:   e.curTrack.Artist,
		Song:     e.curTrack.Song,
		AlbumArt: e.curTrack.AlbumArt,
	}))
}

// ---------------------------------------------------------------------------
// Server-authoritative letter reveal (see reveal.go). All methods below run on
// the Run goroutine; the off-loop timers re-enter via submit and self-cancel on
// a roundKey mismatch.
// ---------------------------------------------------------------------------

// startReveal arms the reveal clock for the current track and kicks off the
// phase-1 noise one-shot. Called from startTrack. It snapshots the live knob
// values so a mid-round config change cannot perturb this round.
func (e *Engine) startReveal() {
	if e.curTrack == nil {
		return
	}
	e.stopRevealTimer()
	e.revealLocked = false // fresh round: buzzing open, points live
	cfg := e.revealCfg.clamp()
	// Start in the fixed-width block phase if the block knob is set (hides the
	// real answer length until it collapses); otherwise show the real-length
	// skeleton immediately (legacy behavior).
	startPhase := revealPhaseSkeleton
	if cfg.BlockMs > 0 {
		startPhase = revealPhaseNoise
	}
	e.rc = revealClock{
		active:      true,
		roundKey:    e.roundKey,
		artist:      upper(e.curTrack.Artist),
		song:        upper(e.curTrack.Song),
		artistOrder: buildRevealOrder(upper(e.curTrack.Artist), seedFromRoundKey(e.roundKey, 0x9e3779b97f4a7c15)),
		songOrder:   buildRevealOrder(upper(e.curTrack.Song), seedFromRoundKey(e.roundKey, 0xc2b2ae3d27d4eb4f)),
		phase:       startPhase,
		genre:       e.curTrack.Genre,
		year:        e.curTrack.Year,
		cfg:         cfg,
	}
	// Broadcast the initial frame (block or skeleton) to stage + mobile.
	e.broadcastMask()

	rk := e.roundKey

	// Pre-reveal hints: show the genre after GenreDelayMs, then the year after a
	// further YearDelayMs (genre first, then year, then the letters). Only armed
	// if the track actually has that datum.
	if e.curTrack.Genre != "" {
		time.AfterFunc(time.Duration(cfg.GenreDelayMs)*time.Millisecond, func() {
			e.submit(func() {
				if e.rc.roundKey != rk || !e.rc.active {
					return
				}
				e.rc.showGenre = true
				e.broadcastMask()
			})
		})
	}
	if e.curTrack.Year > 0 {
		time.AfterFunc(time.Duration(cfg.GenreDelayMs+cfg.YearDelayMs)*time.Millisecond, func() {
			e.submit(func() {
				if e.rc.roundKey != rk || !e.rc.active {
					return
				}
				e.rc.showYear = true
				e.broadcastMask()
			})
		})
	}

	// Block -> skeleton flip: once the block window elapses, reveal the real
	// length (client eases the morph over cfg.EaseMs). Only armed when blocking.
	if cfg.BlockMs > 0 {
		time.AfterFunc(time.Duration(cfg.BlockMs)*time.Millisecond, func() {
			e.submit(func() {
				if e.rc.roundKey != rk || !e.rc.active {
					return
				}
				if e.rc.phase == revealPhaseNoise {
					e.rc.phase = revealPhaseSkeleton
					e.broadcastMask()
				}
			})
		})
	}

	// Letter-stream start: after the phase-1 delay (but never before the block
	// has collapsed), flip to streaming and start the letter ticker.
	time.AfterFunc(time.Duration(cfg.letterStartMs())*time.Millisecond, func() {
		e.submit(func() {
			if e.rc.roundKey != rk || !e.rc.active {
				return
			}
			e.rc.phase1Done = true
			if e.rc.phase < revealPhaseStream {
				e.rc.phase = revealPhaseStream
			}
			e.broadcastMask()
			e.startRevealTicker(rk)
		})
	})
}

// startRevealTicker schedules the next letter tick. Uses a self-rescheduling
// timer so it can be cleanly stopped and so a change of round is inert.
func (e *Engine) startRevealTicker(rk string) {
	if e.rc.allDone() {
		e.rc.active = false
		return
	}
	e.revealTimer = time.AfterFunc(e.rc.revealInterval(), func() {
		e.submit(func() { e.revealTick(rk) })
	})
}

// revealTick advances the reveal by one letter, unless the round changed or is
// paused (buzz in progress). Pausing is just "don't advance": the count-based
// clock resumes automatically when state returns to ROUND_ACTIVE.
func (e *Engine) revealTick(rk string) {
	if e.rc.roundKey != rk || !e.rc.active {
		return // superseded by a new track / cleared
	}
	// Pause-on-buzz: do not advance letters while not actively guessing, but
	// keep the ticker alive so it resumes on return to ROUND_ACTIVE.
	if e.state != protocol.StateRoundActive {
		e.startRevealTicker(rk)
		return
	}
	if e.rc.revealOneLetter() {
		if e.rc.allDone() {
			e.rc.phase = revealPhaseDone
		}
		e.broadcastMask()
	}

	// Lockout gate: once few enough letters remain hidden across BOTH fields the
	// answer is effectively shown — close buzzing and zero the points (fire once).
	if !e.rc.lockedOut && e.rc.remainingHidden() <= e.rc.cfg.LockoutChars {
		e.rc.lockedOut = true
		e.lockOutReveal()
	}

	if e.rc.allDone() {
		e.rc.active = false
		e.scheduleAutoKaraoke(rk) // after full reveal, auto-show lyrics
		return                    // fully revealed; stop the letter ticker
	}
	e.startRevealTicker(rk)
}

// lockOutReveal closes buzzing and zeroes the points for the rest of the round:
// the answer is essentially on screen. Marks every mobile player guessed (so
// their buzzers grey out) and re-broadcasts trackStart with a zero pool so the
// stage/point timer reads 0.
func (e *Engine) lockOutReveal() {
	e.revealLocked = true
	for _, p := range e.reg.mobilePlayers() {
		p.GuessedThisTrack = true
	}
	// Zero-pool trackStart: stage shows 0 points; carry the real lengths.
	artistLen, songLen := 0, 0
	if e.curTrack != nil {
		artistLen, songLen = len(e.curTrack.Artist), len(e.curTrack.Song)
	}
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgTrackStart, protocol.TrackStartData{
		MaxPoints:  0,
		BasePoints: 0,
		StartTime:  e.trackStartMs,
		ArtistLen:  artistLen,
		SongLen:    songLen,
	}))
	// Grey out every mobile buzzer (fresh buzzResult{won:false} clears/locks it).
	for _, p := range e.reg.mobilePlayers() {
		for _, cid := range e.reg.connIDs(p.ID) {
			e.bcast.SendTo(cid, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		}
	}
}

// scheduleAutoKaraoke waits cfg.AutoKaraokeMs after the reveal fully completes,
// then auto-enters karaoke (shows lyrics) — no admin Reveal click needed. Guards
// on roundKey + that we're still in the active round loop.
func (e *Engine) scheduleAutoKaraoke(rk string) {
	if e.rc.cfg.AutoKaraokeMs <= 0 {
		return
	}
	delay := time.Duration(e.rc.cfg.AutoKaraokeMs) * time.Millisecond
	time.AfterFunc(delay, func() {
		e.submit(func() {
			if e.roundKey != rk {
				return // round moved on
			}
			// Only auto-advance from the live-round states; if an admin already
			// graded/revealed/ended, do nothing.
			if e.state == protocol.StateRoundActive || e.state == protocol.StateLocked {
				e.consumeTrack()
				e.buzzWinner = ""
				e.lock.Release(context.Background(), e.roundKey)
				e.enterKaraoke()
			}
		})
	})
}

// broadcastMask builds ONE envelope and fans the identical frame to stage,
// mobile, and admin in the same call. This single-envelope fan-out is the
// mechanism that guarantees mobile is never ahead of the projector (§4A ext).
func (e *Engine) broadcastMask() {
	if !e.rc.active && !e.rc.finalizing {
		return
	}
	env := e.envelope(smsgMaskedReveal, e.rc.currentMask())
	e.bcast.Broadcast(protocol.RoleStage, env)
	e.bcast.Broadcast(protocol.RoleMobile, env)
	e.bcast.Broadcast(protocol.RoleAdmin, env)
}

// finalizeReveal fills all remaining letters and emits the final frame to both
// surfaces (used at KARAOKE / daily double, where the answer becomes public).
func (e *Engine) finalizeReveal() {
	if e.curTrack == nil {
		return
	}
	e.stopRevealTimer()
	// If the clock was never armed (edge case), arm a minimal one so the mask
	// carries the right lengths/order.
	if e.rc.roundKey != e.roundKey || (!e.rc.active && !e.rc.finalizing) {
		e.rc = revealClock{
			roundKey:    e.roundKey,
			artist:      upper(e.curTrack.Artist),
			song:        upper(e.curTrack.Song),
			artistOrder: buildRevealOrder(upper(e.curTrack.Artist), seedFromRoundKey(e.roundKey, 0x9e3779b97f4a7c15)),
			songOrder:   buildRevealOrder(upper(e.curTrack.Song), seedFromRoundKey(e.roundKey, 0xc2b2ae3d27d4eb4f)),
			cfg:         e.revealCfg.clamp(),
		}
	}
	e.rc.finalizing = true
	e.rc.completeAll()
	e.broadcastMask()
	e.rc.active = false
}

// revealFieldFully forces one field (artist|song) to fully reveal — used on a
// partial grade so both surfaces settle that field simultaneously.
func (e *Engine) revealFieldFully(field string) {
	if !e.rc.active {
		return
	}
	switch field {
	case "artist":
		e.rc.artistRevealed = len(e.rc.artistOrder)
	case "song":
		e.rc.songRevealed = len(e.rc.songOrder)
	default:
		return
	}
	if e.rc.allDone() {
		e.rc.phase = revealPhaseDone
	}
	e.broadcastMask()
}

// clearReveal tears down the reveal clock and stops its timer. The roundKey
// guard already neutralizes a stale fire, but stopping is tidy.
func (e *Engine) clearReveal() {
	e.stopRevealTimer()
	e.rc = revealClock{}
}

func (e *Engine) stopRevealTimer() {
	if e.revealTimer != nil {
		e.revealTimer.Stop()
		e.revealTimer = nil
	}
}

func (e *Engine) adminViewEnvelope() protocol.ServerEnvelope {
	av := protocol.AdminViewData{}
	if e.curTrack != nil {
		av.CorrectArtist = e.curTrack.Artist
		av.CorrectSong = e.curTrack.Song
		// From the live pool, so the grader sees what CORRECT will actually pay
		// rather than the un-reduced row value.
		maxP, baseP := e.roundPool()
		av.CurrentPoints = currentPointsFromPool(maxP, baseP, e.pausedAtMs)
	}
	if w := e.reg.players[e.buzzWinner]; w != nil {
		av.BuzzedPlayerID = w.ID
		av.BuzzedHandle = w.Handle
	}
	return e.envelope(protocol.SMsgAdminView, av)
}

func (e *Engine) scoreboardData() protocol.ScoreboardData {
	players := []protocol.ScoreEntry{}
	for _, p := range e.reg.players {
		if e.reg.isMobile(p.ID) {
			players = append(players, protocol.ScoreEntry{ID: p.ID, Handle: p.Handle, Score: p.Score})
		}
	}
	return protocol.ScoreboardData{Players: players}
}

// MobilePlayerCount returns the number of live mobile (buzzer) players, for
// the public landing page's "N jacked in" count. Safe from any goroutine —
// routed through the Run loop's command channel like StartGame/ResetToLobby,
// so it reads the same authoritative registry the scoreboard does with no
// separate counter to keep in sync.
func (e *Engine) MobilePlayerCount() int {
	type result struct{ n int }
	ch := make(chan result, 1)
	e.cmds <- command{fn: func() {
		n := 0
		for _, p := range e.reg.players {
			if e.reg.isMobile(p.ID) {
				n++
			}
		}
		ch <- result{n}
	}}
	return (<-ch).n
}

// broadcastScoreboard sends scores to ALL roles. The scoreboard carries only
// player handles + scores (no track metadata), so it is §4A-safe for mobile —
// players want to see their own standing.
func (e *Engine) broadcastScoreboard() {
	env := e.envelope(protocol.SMsgScoreboard, e.scoreboardData())
	e.bcast.Broadcast(protocol.RoleStage, env)
	e.bcast.Broadcast(protocol.RoleAdmin, env)
	e.bcast.Broadcast(protocol.RoleMobile, env)
}

func (e *Engine) broadcastBoard() {
	env := e.envelope(protocol.SMsgBoard, boardData(e.board))
	e.bcast.Broadcast(protocol.RoleStage, env)
	e.bcast.Broadcast(protocol.RoleAdmin, env)
}

func (e *Engine) telemetryData() protocol.TelemetryData {
	conns := []protocol.TelemetryConn{}
	for _, p := range e.reg.players {
		if !e.reg.isMobile(p.ID) {
			continue
		}
		conns = append(conns, protocol.TelemetryConn{
			ID:     p.ID,
			Handle: p.Handle,
			RTTMs:  p.RTTMs,
			Score:  p.Score,
			Active: p.Active && e.reg.online(p.ID),
		})
	}
	return protocol.TelemetryData{Connections: conns}
}

func (e *Engine) broadcastTelemetry() {
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(protocol.SMsgTelemetry, e.telemetryData()))
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgCheatReport, e.cheatReportData()))
}

// playerIP returns the IP of any live connection for a player ("" if none).
func (e *Engine) playerIP(playerID string) string {
	for _, cid := range e.reg.connIDs(playerID) {
		if ip := e.connIP[cid]; ip != "" {
			return ip
		}
	}
	return ""
}

// cheatReportData builds the admin anti-cheat snapshot: per mobile player, its
// IP + live connection count, flagged for shared-IP (another player on the same
// IP) and multi-conn (more than one live socket — e.g. a second tab peeking at
// the stage). Cheap heuristics, meant as a "look here" nudge, not proof.
func (e *Engine) cheatReportData() cheatReportData {
	// Count how many distinct mobile players share each IP.
	ipPlayers := map[string]int{}
	for _, p := range e.reg.players {
		if !e.reg.isMobile(p.ID) {
			continue
		}
		if ip := e.playerIP(p.ID); ip != "" {
			ipPlayers[ip]++
		}
	}

	out := []cheatEntry{}
	for _, p := range e.reg.players {
		if !e.reg.isMobile(p.ID) {
			continue
		}
		ip := e.playerIP(p.ID)
		conns := len(e.reg.connIDs(p.ID))
		flags := []string{}
		if ip != "" && ipPlayers[ip] > 1 {
			flags = append(flags, "shared-ip")
		}
		if conns > 1 {
			flags = append(flags, "multi-conn")
		}
		out = append(out, cheatEntry{
			PlayerID: p.ID,
			Handle:   p.Handle,
			IP:       ip,
			Conns:    conns,
			Flags:    flags,
		})
	}
	return cheatReportData{Players: out}
}

func (e *Engine) persistScores() {
	for _, p := range e.reg.players {
		if e.reg.isMobile(p.ID) {
			_ = e.repo.SaveScore(context.Background(), e.cfg.SessionID, p.ID, p.Handle, p.Score)
		}
	}
}

// ---------------------------------------------------------------------------
// State transitions + envelopes
// ---------------------------------------------------------------------------

// transitionTo bumps the nonce (§4D) on every state change and broadcasts the
// sanitized StateData to ALL clients (the only all-roles broadcast, §4A).
func (e *Engine) transitionTo(s protocol.GameState) {
	e.state = s
	e.session.State = string(s)
	e.gate.Bump()
	e.bcast.BroadcastAll(e.envelope(protocol.SMsgState, protocol.StateData{State: s}))
}

// envelope builds a ServerEnvelope with the current nonce + a per-engine seq.
func (e *Engine) envelope(t protocol.ServerMsgType, data any) protocol.ServerEnvelope {
	raw, _ := json.Marshal(data)
	e.seq++
	return protocol.ServerEnvelope{Type: t, Data: raw, Nonce: e.gate.Current(), Seq: e.seq}
}

func (e *Engine) sendError(connID, code, msg string) {
	e.bcast.SendTo(connID, e.envelope(protocol.SMsgError, protocol.ErrorData{Code: code, Message: msg}))
}

func (e *Engine) sendErrorAll(code, msg string) {
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(protocol.SMsgError, protocol.ErrorData{Code: code, Message: msg}))
}

// nowMs returns the server wall clock in epoch milliseconds.
func nowMs() int64 { return time.Now().UnixMilli() }

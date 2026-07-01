package game

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/anticheat"
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
	RevealAlternate    bool
	RevealAlternateSet bool
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

// Engine implements game.InboundHandler and drives the full lifecycle.
type Engine struct {
	repo   GameRepo
	lock   BuzzLock
	audio  AudioDevice
	lyrics LyricsProvider
	bcast  Broadcaster
	gate   *anticheat.NonceGate
	cfg    Config
	rng    *rand.Rand

	cmds       chan command
	seq        uint64
	roleSetter RoleSetter // promotes a conn's role in the transport on Hello

	// ---- live state (Run-goroutine-owned) ----
	state   protocol.GameState
	board   *Board
	reg     *registry
	session *Session

	curCell      *Cell
	curTrack     *Track
	curRow       int
	roundKey     string // BuzzLock key for the active buzz round
	trackStartMs int64  // server epoch ms when the current track started
	pausedAtMs   int64  // playback position when paused on a buzz
	buzzWinner   string // playerID currently holding the lock
	partial      pendingPartial

	cellPicker string // playerID who earns next cell selection ("" = admin only)

	// skip voting (§3.8)
	votingPool map[string]bool // playerIDs eligible to vote this karaoke phase
	votes      map[string]bool // playerIDs who have voted

	// daily double (§7)
	ratingPool map[string]bool
	ratings    map[string]int // playerID -> stars

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
}

// NewEngine wires the engine to its dependencies (all injected for testing).
func NewEngine(repo GameRepo, lock BuzzLock, audio AudioDevice, lyrics LyricsProvider, bcast Broadcaster, gate *anticheat.NonceGate, cfg Config) *Engine {
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
	// Seed the reveal-timing knobs from config, falling back to defaults.
	revCfg := defaultRevealConfig()
	if cfg.RevealIntervalMs > 0 {
		revCfg.IntervalMs = cfg.RevealIntervalMs
	}
	if cfg.RevealPhase1Ms > 0 {
		revCfg.Phase1Ms = cfg.RevealPhase1Ms
	}
	if cfg.RevealAlternateSet {
		revCfg.Alternate = cfg.RevealAlternate
	}
	return &Engine{
		repo:      repo,
		lock:      lock,
		audio:     audio,
		lyrics:    lyrics,
		bcast:     bcast,
		gate:      gate,
		cfg:       cfg,
		rng:       rng,
		cmds:      make(chan command, 256),
		state:     protocol.StateLobby,
		reg:       newRegistry(),
		session:   &Session{ID: cfg.SessionID, SkipThresholdPct: cfg.SkipThresholdPct, State: string(protocol.StateLobby)},
		revealCfg: revCfg.clamp(),
	}
}

// Run is the single command loop (§9). All state mutation happens here. It
// returns when ctx is cancelled. The engine must be running before inbound
// calls do useful work; OnConnect/OnMessage/OnDisconnect enqueue onto cmds.
func (e *Engine) Run(ctx context.Context) error {
	if e.board == nil {
		if b, err := e.repo.LoadBoard(ctx, e.cfg.SessionID); err == nil && b != nil {
			e.board = b
		}
	}
	_ = e.repo.CreateSession(ctx, e.session)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case c := <-e.cmds:
			c.fn()
			if c.done != nil {
				close(c.done)
			}
		}
	}
}

// SetBoard injects a board directly (test/admin bootstrap). Safe before Run.
func (e *Engine) SetBoard(b *Board) { e.board = b }

// ReloadBoard atomically replaces the active board via the Run loop's command
// channel. Safe to call from any goroutine (e.g., the admin REST handler).
// After replacement, broadcasts the new grid to stage/admin clients.
func (e *Engine) ReloadBoard(b *Board) {
	e.submit(func() {
		e.board = b
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
		if e.state != protocol.StateLobby {
			ch <- result{fmt.Errorf("game already started (state: %s)", e.state)}
			return
		}
		e.transitionTo(protocol.StateBoard)
		e.broadcastBoard()
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
		e.cellPicker = ""
		e.votingPool = nil
		e.votes = nil
		e.ratingPool = nil
		e.ratings = nil
		// Reset all player scores
		for _, p := range e.reg.players {
			p.Score = 0
			p.GuessedThisTrack = false
			p.IdleRounds = 0
		}
		// Reset board track played state
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
		}
		// Unload the board so admin must explicitly re-attach
		e.board = nil
		e.transitionTo(protocol.StateLobby)
		e.broadcastScoreboard()
		e.broadcastBoard()
		ch <- result{}
	}}
	return (<-ch).err
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

// submit enqueues fn for the Run loop. It does not wait.
func (e *Engine) submit(fn func()) {
	select {
	case e.cmds <- command{fn: fn}:
	default:
		// Backpressure: run inline only if the loop is saturated. This should
		// be vanishingly rare; the channel is generously buffered.
		e.cmds <- command{fn: fn}
	}
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
func (e *Engine) OnConnect(connID string) {
	e.submit(func() { e.reg.addConn(connID) })
}

// OnMessage forwards a decoded client frame. arrivalUnixMs is the SERVER
// arrival clock used for buzz ordering (§4B).
func (e *Engine) OnMessage(connID string, role protocol.Role, env protocol.ClientEnvelope, arrivalUnixMs int64) {
	e.submit(func() { e.dispatch(connID, role, env, arrivalUnixMs) })
}

// OnDisconnect removes a connection and recalculates the active pool (§3.8).
func (e *Engine) OnDisconnect(connID string) {
	e.submit(func() {
		playerID, fullyGone := e.reg.removeConn(connID)
		if playerID != "" && fullyGone {
			// Drop from active vote/rating pools and recompute thresholds (§3.8).
			delete(e.votingPool, playerID)
			delete(e.ratingPool, playerID)
			if p := e.reg.players[playerID]; p != nil {
				p.Active = false
			}
			if e.state == protocol.StateKaraoke {
				e.evaluateSkipVotes()
			}
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
		cmsgAdminSetRevealCfg:
		if role != protocol.RoleAdmin {
			e.sendError(connID, "forbidden", "admin role required")
			return
		}
		e.dispatchAdmin(connID, env)

	case protocol.CMsgStagePlayerState:
		e.onStagePlayerState(connID, env)
	case protocol.CMsgStageDeviceReady:
		e.onStageDeviceReady(connID, env)
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
	case protocol.CMsgAdminEndGame:
		e.transitionTo(protocol.StateGameOver)
		e.persistScores()
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
	if (h.Role == protocol.RoleAdmin || h.Role == protocol.RoleStage) && e.cfg.AdminSecret != "" && h.AdminSecret != e.cfg.AdminSecret {
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
	e.broadcastScoreboard()
	e.broadcastBoard()
}

func (e *Engine) onHeartbeat(connID string, env protocol.ClientEnvelope) {
	var hb protocol.HeartbeatData
	_ = json.Unmarshal(env.Data, &hb)
	now := nowMs()
	// Refocus reinstates an idled player into the active pool (§3.8).
	if p := e.reg.playerForConn(connID); p != nil {
		p.IdleRounds = 0
		if e.reg.isMobile(p.ID) {
			p.Active = true
		}
	}
	// clientTime is used ONLY for RTT, never ordering (§4B). We do not trust it
	// for arrival; transport stamps arrival. Here we just echo a server clock.
	e.bcast.SendTo(connID, e.envelope(protocol.SMsgHeartbeat, map[string]int64{
		"serverTime": now,
	}))
}

// sendFullSync delivers an audience-scoped FULL_STATE_SYNC (§9). Mobile gets
// only the sanitized state flag; stage/admin additionally get board + reveal.
func (e *Engine) sendFullSync(connID string, role protocol.Role) {
	e.bcast.SendTo(connID, e.envelope(protocol.SMsgState, protocol.StateData{State: e.state}))
	// Reconnect mid-reveal: send the CURRENT masked frame to ALL roles (mobile
	// included) so a reconnecting phone resyncs to exactly what the stage shows.
	// currentMask reflects only rc.*Revealed, so this can never leak ahead.
	if (e.rc.active || e.rc.finalizing) && e.curTrack != nil {
		e.bcast.SendTo(connID, e.envelope(smsgMaskedReveal, e.rc.currentMask()))
	}
	// Echo current reveal-timing knob values to the admin control room so its
	// sliders reflect server truth on (re)connect.
	if role == protocol.RoleAdmin {
		e.bcast.SendTo(connID, e.envelope(smsgAdminRevealCfg, e.revealCfgData()))
	}
	if protocol.TrustedReveal(role) {
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgBoard, boardData(e.board)))
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgScoreboard, e.scoreboardData()))
		if e.curTrack != nil {
			e.bcast.SendTo(connID, e.trackStartEnvelope())
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
// round (§7). The cell stays live until its pool is exhausted (§7 persistent
// cells). Only valid from BOARD/KARAOKE/TRANSITION/etc. (not mid-round).
func (e *Engine) selectCell(row, col int) {
	if e.state == protocol.StateRoundActive || e.state == protocol.StateLocked || e.state == protocol.StateAdjudicate {
		// A round is live; selection is queued by the admin only between rounds.
		e.sendErrorAll("busy", "round in progress")
		return
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
	track := pickTrack(cell, e.rng)
	if track == nil {
		e.sendErrorAll("exhausted", "cell pool exhausted")
		return
	}
	e.startTrack(cell, track)
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
	e.partial = pendingPartial{}
	e.roundKey = fmt.Sprintf("%s:r%dc%d:%s:%d", e.cfg.SessionID, cell.Row, cell.Col, track.ID, e.trackStartMs)

	// New track => everyone may guess again (§3.4 one guess per track).
	for _, p := range e.reg.players {
		p.GuessedThisTrack = false
	}

	e.transitionTo(protocol.StateRoundActive)

	// trackStart carries point ceilings + answer LENGTHS for the stage timer.
	// The letter reveal is now server-authoritative: startReveal arms the clock
	// and streams masked frames to BOTH stage and mobile (see reveal.go). We no
	// longer send the full answer text to the stage during ROUND_ACTIVE — the
	// mask stream is the only reveal source, so a hostile client cannot read
	// ahead from the stage's memory either.
	e.bcast.Broadcast(protocol.RoleStage, e.trackStartEnvelope())
	e.startReveal()
	e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())

	// Route playback to the stage's virtual device (§6, §9).
	if err := e.audio.Play(context.Background(), track.SpotifyURI, 0); err != nil {
		log.Printf("[engine] audio.Play failed: %v", err)
	}
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{
		Action: "play", TrackURI: track.SpotifyURI, PositionMs: 0,
	}))
}

func (e *Engine) trackStartEnvelope() protocol.ServerEnvelope {
	return e.envelope(protocol.SMsgTrackStart, protocol.TrackStartData{
		MaxPoints:  MaxPointsForRow(e.curRow),
		BasePoints: BaseValue,
		StartTime:  e.trackStartMs,
		ArtistLen:  len(e.curTrack.Artist),
		SongLen:    len(e.curTrack.Song),
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

	// §4B/§4C: compute the latency-compensated effective time. We are already
	// serialized on the Run loop, so the FIRST buzz dispatched here is the first
	// to reach the lock — but we still go through BuzzLock so the atomic
	// single-winner guarantee holds even across a distributed Redis backend.
	_ = anticheat.EffectiveBuzzTime(arrivalMs, p.RTTMs)

	won, err := e.lock.TryAcquire(context.Background(), e.roundKey, p.ID)
	if err != nil || !won {
		e.bcast.SendTo(connID, e.envelope(protocol.SMsgBuzzResult, protocol.BuzzResultData{Won: false}))
		return
	}

	// Winner. Pause audio IMMEDIATELY via the direct stage path (~20ms, §9),
	// NOT the Spotify API round-trip.
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
	pts := CurrentPoints(e.curRow, elapsed)
	if e.partial.active {
		// A prior partial already took 50; the remaining-half guesser claims
		// the leftover pool snapshot decayed to now (§7).
		pts = e.partial.remaining
		if pts < 0 {
			pts = 0
		}
	}
	e.award(winner, pts)
	e.cellPicker = winner.ID
	e.buzzWinner = ""
	e.lock.Release(context.Background(), e.roundKey)
	e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())

	e.curTrack.Played = true // consume this track from the pool (§7)

	if e.curCell.DailyDouble {
		e.enterDailyDouble(winner)
		return
	}
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

	if secondPartial {
		// Two partials exhaust guessing; clear admin evaluation and enter karaoke.
		e.buzzWinner = ""
		e.lock.Release(context.Background(), e.roundKey)
		e.bcast.Broadcast(protocol.RoleAdmin, e.adminViewEnvelope())
		e.curTrack.Played = true
		e.enterKaraoke()
		return
	}

	// Force the graded field fully revealed on BOTH surfaces via the mask, and
	// keep the stage-only cosmetic signal for its existing settle handling.
	if kind != "" {
		e.revealFieldFully(kind)
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgPartialReveal, map[string]string{"field": kind}))
	}
	e.resumeAudio()
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
func (e *Engine) award(p *Player, pts int) {
	p.Score += pts
	_ = e.repo.SaveScore(context.Background(), e.cfg.SessionID, p.ID, p.Handle, p.Score)
	e.broadcastScoreboard()
}

// ---------------------------------------------------------------------------
// Karaoke + skip voting (§3.7, §3.8)
// ---------------------------------------------------------------------------

// adminReveal is the admin force-revealing the answer mid-round. It enters
// karaoke (shows answer + lyrics, disables guessing) without awarding points.
func (e *Engine) adminReveal() {
	if e.curTrack != nil {
		e.curTrack.Played = true
	}
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
}

func (e *Engine) fetchAndSendLyrics() {
	if e.lyrics == nil || e.curTrack == nil {
		return
	}
	lines, err := e.lyrics.Fetch(context.Background(), e.curTrack.Artist, e.curTrack.Song, int(e.curTrack.DurationMs/1000))
	if err != nil || lines == nil {
		return
	}
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgLyrics, protocol.LyricsData{Lines: lines}))
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
		e.beginTransition()
	}
}

// beginTransition stops audio, shows the countdown, then the loop returns to
// the board for the next selection (§3.9). The actual next track is admin-
// selected from the ready queue; here we just stop + reset to BOARD.
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
				e.transitionTo(protocol.StateBoard)
				e.broadcastBoard()
			}
		})
	}()
}

// ---------------------------------------------------------------------------
// Daily Double (§7)
// ---------------------------------------------------------------------------

func (e *Engine) enterDailyDouble(performer *Player) {
	e.transitionTo(protocol.StateDailyDouble)
	e.resumeAudio()
	// The answer is already earned; complete the streamed reveal to both
	// surfaces, plus the trusted full reveal to the stage.
	e.finalizeReveal()
	e.revealTo(protocol.RoleStage)
	e.cellPicker = performer.ID

	// Active users rate; the performer does not rate themselves.
	e.ratingPool = map[string]bool{}
	e.ratings = map[string]int{}
	for _, p := range e.reg.mobilePlayers() {
		if p.ID == performer.ID || p.Banned || !e.reg.online(p.ID) {
			continue
		}
		e.ratingPool[p.ID] = true
	}
	// If nobody can rate, skip straight to karaoke.
	if len(e.ratingPool) == 0 {
		e.enterKaraoke()
	}
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

	// Once every eligible rater has voted, average + apply the bonus (§7).
	if len(e.ratings) >= len(e.ratingPool) {
		e.finishDailyDouble()
	}
}

func (e *Engine) finishDailyDouble() {
	performer := e.reg.players[e.cellPicker]
	if performer != nil && len(e.ratings) > 0 {
		sum := 0
		for _, s := range e.ratings {
			sum += s
		}
		avg := float64(sum) / float64(len(e.ratings))
		mult := DailyDoubleMultiplier(avg)
		bonus := int(float64(MaxPointsForRow(e.curRow)) * (mult - 1.0))
		e.award(performer, bonus)
		_ = e.repo.LogEvent(context.Background(), e.cfg.SessionID, "dailyDouble", map[string]any{
			"playerID": performer.ID, "avgStars": avg, "bonus": bonus,
		})
	}
	e.enterKaraoke()
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
	switch d.Action {
	case "pause":
		_ = e.audio.Pause(context.Background())
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
	case "resume", "play":
		e.resumeAudio()
	}
}

func (e *Engine) onAdminAward(connID string, env protocol.ClientEnvelope) {
	var d protocol.AdminAwardData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badAward", err.Error())
		return
	}
	if p := e.reg.players[d.PlayerID]; p != nil {
		p.Score += d.Delta
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
	for _, cid := range e.reg.connIDs(p.ID) {
		e.bcast.SendTo(cid, e.envelope(protocol.SMsgError, protocol.ErrorData{Code: "kicked", Message: "removed by admin"}))
	}
	if e.state == protocol.StateKaraoke {
		e.evaluateSkipVotes()
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
	return adminRevealCfgData{IntervalMs: c.IntervalMs, Phase1Ms: c.Phase1Ms, Alternate: c.Alternate}
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
	if d.Alternate != nil {
		cfg.Alternate = *d.Alternate
	}
	e.revealCfg = cfg.clamp()
	// Echo the applied values to every admin so all control-room tabs agree.
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgAdminRevealCfg, e.revealCfgData()))
}

// endRound force-ends the current round and returns to the board (§3.10).
func (e *Engine) endRound() {
	if e.curTrack != nil {
		e.curTrack.Played = true
	}
	e.lock.Release(context.Background(), e.roundKey)
	_ = e.audio.Pause(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
	e.clearReveal()
	e.curTrack = nil
	e.curCell = nil
	e.buzzWinner = ""
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
	if !d.TrackEnded {
		return
	}
	switch e.state {
	case protocol.StateKaraoke:
		e.beginTransition()
	case protocol.StateRoundActive:
		e.endRound()
	}
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
	cfg := e.revealCfg.clamp()
	e.rc = revealClock{
		active:      true,
		roundKey:    e.roundKey,
		artist:      upper(e.curTrack.Artist),
		song:        upper(e.curTrack.Song),
		artistOrder: buildRevealOrder(upper(e.curTrack.Artist), seedFromRoundKey(e.roundKey, 0x9e3779b97f4a7c15)),
		songOrder:   buildRevealOrder(upper(e.curTrack.Song), seedFromRoundKey(e.roundKey, 0xc2b2ae3d27d4eb4f)),
		phase:       revealPhaseSkeleton, // length + spaces visible immediately
		cfg:         cfg,
	}
	// Broadcast the initial length-skeleton frame to stage + mobile.
	e.broadcastMask()

	// Phase-1 one-shot: after the noise delay, flip to streaming and start the
	// letter ticker. Capture roundKey to neutralize a stale fire.
	rk := e.roundKey
	delay := e.rc.phase1Delay()
	e.revealTimer = time.AfterFunc(delay, func() {
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
	if e.rc.allDone() {
		e.rc.active = false
		return // fully revealed; stop (no re-arm)
	}
	e.startRevealTicker(rk)
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
		av.CurrentPoints = CurrentPoints(e.curRow, e.pausedAtMs)
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

// broadcastScoreboard sends scores to trusted roles only (handles aren't track
// metadata, but the scoreboard is a stage/admin view; mobile shows nothing).
func (e *Engine) broadcastScoreboard() {
	env := e.envelope(protocol.SMsgScoreboard, e.scoreboardData())
	e.bcast.Broadcast(protocol.RoleStage, env)
	e.bcast.Broadcast(protocol.RoleAdmin, env)
}

func (e *Engine) broadcastBoard() {
	env := e.envelope(protocol.SMsgBoard, boardData(e.board))
	e.bcast.Broadcast(protocol.RoleStage, env)
	e.bcast.Broadcast(protocol.RoleAdmin, env)
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

package game

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// solo_karaoke.go — the second, independent game mode (straight playback,
// no guessing/scoring). Mutually exclusive with the trivia state machine via
// a server-side mode-lock (modeLockOwner/broadcastModeLock): starting
// trivia is rejected while Solo Karaoke is active, and starting/playing
// Solo Karaoke is rejected while a trivia game is active (state not
// LOBBY/GAME_OVER). The lock is released only by an explicit action
// (admin.soloKaraoke.stop or queue exhaustion, End Game) — never by
// navigation, tab close, or unload; admin can have several /admin tabs open
// at once, so every tab must reflect the SAME server-side lock via the
// modeLock broadcast rather than any local/per-tab state.
//
// Naming: internal/wire identifiers use "Solo Karaoke" / SoloKaraoke... —
// deliberately distinct from protocol.StateKaraoke ("KARAOKE"), which is the
// TRIVIA game's post-round lyrics/skip-vote phase (state.go). No collision:
// different Go identifiers, different wire strings.
//
// CONTRACT-QUESTION: the nine cmsgAdminSoloKaraoke* client message types and
// the four smsg* server message types below are new, local to this package,
// per project rules (protocol.go is a fixed contract). If accepted they
// should move into protocol.go on a version bump. Mirrored in
// web/shared/protocol.ts; see docs/CHANGELOG.md.
const (
	cmsgAdminSoloKaraokePlay       protocol.ClientMsgType = "admin.soloKaraoke.play"
	cmsgAdminSoloKaraokePause      protocol.ClientMsgType = "admin.soloKaraoke.pause"
	cmsgAdminSoloKaraokeResume     protocol.ClientMsgType = "admin.soloKaraoke.resume"
	cmsgAdminSoloKaraokeStop       protocol.ClientMsgType = "admin.soloKaraoke.stop"
	cmsgAdminSoloKaraokeNext       protocol.ClientMsgType = "admin.soloKaraoke.next"
	cmsgAdminSoloKaraokePrev       protocol.ClientMsgType = "admin.soloKaraoke.prev"
	cmsgAdminSoloKaraokeSeek       protocol.ClientMsgType = "admin.soloKaraoke.seek"
	cmsgAdminSoloKaraokeSetShuffle protocol.ClientMsgType = "admin.soloKaraoke.setShuffle"
	cmsgAdminSoloKaraokeSetRepeat  protocol.ClientMsgType = "admin.soloKaraoke.setRepeat"
)

// CONTRACT-QUESTION: smsgSoloKaraokeNowPlaying carries full track metadata
// for the current Solo Karaoke track. Sent ONLY via Broadcast(RoleStage, ...)
// and Broadcast(RoleAdmin, ...) — NEVER BroadcastAll — since mobile must
// never receive track title/artist/URI (§4A), and there is no TrustedReveal
// call site to copy for this new message family.
const smsgSoloKaraokeNowPlaying protocol.ServerMsgType = "soloKaraokeNowPlaying"

// CONTRACT-QUESTION: smsgModeLock tells the admin control room which mode
// currently owns the engine ("none" | "trivia" | "solo_karaoke"), so every
// open /admin tab's "Start Game" / "Play" buttons disable in lockstep off a
// server broadcast rather than per-tab local state (navigation/tab-close is
// not a reliable release signal — see the file doc comment). Admin-only;
// carries no track metadata.
const smsgModeLock protocol.ServerMsgType = "modeLock"

// CONTRACT-QUESTION: smsgSoloKaraokeActive is the ONLY Solo Karaoke signal
// mobile may ever receive — a bare boolean, zero track metadata — sent via
// BroadcastAll so the mobile app can show an idle "sing along at the stage"
// screen instead of its normal GameState-driven UI.
const smsgSoloKaraokeActive protocol.ServerMsgType = "soloKaraokeActive"

// CONTRACT-QUESTION: smsgSoloKaraokeLyricsStatus is Solo Karaoke's own
// loading/ready/none lyrics-status signal, kept distinct from reveal.go's
// smsgLyricsStatus (the trivia game's post-round karaoke phase) per the
// project's naming decision — the two modes must never share an identifier.
// Stage-only. The lines themselves still arrive via the fixed SMsgLyrics
// frame, exactly like trivia's karaoke phase.
const smsgSoloKaraokeLyricsStatus protocol.ServerMsgType = "soloKaraokeLyricsStatus"

// CONTRACT-QUESTION: smsgSoloKaraokeError surfaces a playback failure
// (audio.Play erroring, e.g. a transient Spotify rate limit or an
// unregistered device) to the admin Player UI (s10eng-07). Without this,
// such a failure was only ever logged server-side — the session stayed
// "active" claiming to play a track that never actually started, with no
// TrackEnded ever arriving to trigger auto-advance, until a human noticed
// the silence at a live event. Admin-only; carries no track metadata beyond
// a plain message string.
const smsgSoloKaraokeError protocol.ServerMsgType = "soloKaraokeError"

// soloKaraokePlayData is the admin.soloKaraoke.play payload. TrackID is
// optional: "" resumes/starts at the current index (or 0 if none), set
// jumps directly to that playlist track (the admin clicking a specific song
// in the Player UI).
type soloKaraokePlayData struct {
	TrackID string `json:"trackId"`
}

type soloKaraokeSeekData struct {
	PositionMs int64 `json:"positionMs"`
}

type soloKaraokeSetShuffleData struct {
	Shuffle bool `json:"shuffle"`
}

// soloKaraokeSetRepeatData.Repeat is one of "" (no repeat), "one" (replay
// the current track on end), "all" (wrap to the start of the queue once the
// end is reached). Validated in onAdminSoloKaraokeSetRepeat.
type soloKaraokeSetRepeatData struct {
	Repeat string `json:"repeat"`
}

// modeLockData.Owner is one of "none" | "trivia" | "solo_karaoke".
type modeLockData struct {
	Owner string `json:"owner"`
}

type soloKaraokeActiveData struct {
	Active bool `json:"active"`
}

// soloKaraokeNowPlayingData carries full track metadata — stage/admin only,
// see smsgSoloKaraokeNowPlaying's CONTRACT-QUESTION above. Index/QueueLen
// let the Player UI render "track 3 of 12" and enable/disable prev/next
// without a separate round-trip. Index is -1 when nothing is loaded.
type soloKaraokeNowPlayingData struct {
	TrackID    string `json:"trackId"`
	SpotifyURI string `json:"spotifyUri"`
	Artist     string `json:"artist"`
	Song       string `json:"song"`
	AlbumArt   string `json:"albumArt"`
	DurationMs int64  `json:"durationMs"`
	Index      int    `json:"index"`
	QueueLen   int    `json:"queueLen"`
	Shuffle    bool   `json:"shuffle"`
	Repeat     string `json:"repeat"`
	// Paused lets a (re)connecting admin tab's Play/Pause toggle render
	// correctly from this one frame alone (s10eng-05) — without it, the only
	// other paused signal (broadcastPlaybackPosition) is reactive to the
	// stage's own periodic position reports and isn't sent proactively on
	// connect, so a reconnecting admin had no authoritative state until the
	// next tick happened to arrive.
	Paused bool `json:"paused"`
}

type soloKaraokeLyricsStatusData struct {
	Status string `json:"status"` // "loading" | "ready" | "none"
}

type soloKaraokeErrorData struct {
	Message string `json:"message"`
}

// soloKaraokeState is the live state for Solo Karaoke mode (Run-goroutine-
// owned, exactly like every other live Engine field). trackStartMs/
// pausedAtMs/paused are kept SEPARATE from trivia's e.trackStartMs/
// e.pausedAtMs rather than reused: the two modes are mutually exclusive in
// time (mode-lock), but trivia's fields carry round-bookkeeping semantics
// (decay clock, buzz-window anchoring) that Solo Karaoke has no business
// touching, and keeping them apart avoids any accidental coupling if that
// invariant is ever loosened later. playToken mirrors roundKey's role as a
// staleness guard for the async lyric fetch.
type soloKaraokeState struct {
	active       bool
	currentTrack *Track
	queue        []*Track
	index        int // index of currentTrack within queue; -1 when none loaded
	shuffle      bool
	repeat       string // "" | "one" | "all"
	playToken    string
	paused       bool
	pausedAtMs   int64
	trackStartMs int64
}

// ReloadSoloKaraokeQueue replaces the live Solo Karaoke queue with tracks
// (the admin REST handler's current view of the Playlist bucket, called
// after every playlist mutation — add/delete/reorder/import — so the
// engine never queries Postgres itself; see admin.SoloKaraokeReloader).
// Safe from any goroutine. If the currently-playing track is still present
// in the new queue, index is updated to its new position so prev/next still
// work from the "same" track; if it was removed, playback is left running
// undisturbed (no yanked audio) but index becomes -1 until the next
// play/next/prev picks a fresh track.
func (e *Engine) ReloadSoloKaraokeQueue(tracks []*Track) {
	e.submit(func() {
		e.soloKaraoke.queue = tracks
		if e.soloKaraoke.currentTrack == nil {
			return
		}
		for i, t := range tracks {
			if t.ID == e.soloKaraoke.currentTrack.ID {
				e.soloKaraoke.index = i
				return
			}
		}
		e.soloKaraoke.index = -1
	})
}

// onAdminSoloKaraokePlay starts (or jumps within) Solo Karaoke playback.
// Gated by the mode-lock: rejected while a trivia game is active (state not
// LOBBY/GAME_OVER) — the first-play entry point into the mode. Once active,
// e.state cannot change out from under it (StartGame itself is gated on
// e.soloKaraoke.active), so later actions on an already-active session need
// no additional state check here.
func (e *Engine) onAdminSoloKaraokePlay(connID string, env protocol.ClientEnvelope) {
	if e.state != protocol.StateLobby && e.state != protocol.StateGameOver {
		e.sendError(connID, "modeLocked", "a trivia game is active — end it before starting Solo Karaoke")
		return
	}
	if len(e.soloKaraoke.queue) == 0 {
		e.sendError(connID, "emptyQueue", "the Solo Karaoke playlist is empty")
		return
	}
	var d soloKaraokePlayData
	_ = json.Unmarshal(env.Data, &d) // TrackID is optional; a malformed/empty body just means "play current"

	idx := e.soloKaraoke.index
	if d.TrackID != "" {
		found := -1
		for i, t := range e.soloKaraoke.queue {
			if t.ID == d.TrackID {
				found = i
				break
			}
		}
		if found == -1 {
			e.sendError(connID, "trackNotFound", "track not in the Solo Karaoke playlist")
			return
		}
		idx = found
	} else if idx < 0 || idx >= len(e.soloKaraoke.queue) {
		idx = 0
	}

	wasActive := e.soloKaraoke.active
	e.soloKaraoke.active = true
	e.soloKaraokePlayAt(idx)
	if !wasActive {
		e.broadcastSoloKaraokeActive()
		e.broadcastModeLock()
	}
}

// soloKaraokePlayAt loads and plays e.soloKaraoke.queue[idx], resetting the
// per-track bookkeeping (position clock, lyric staleness token) and
// broadcasting the new now-playing state. Shared by every path that starts
// a (possibly new) track: the initial play, next/prev, and auto-advance.
func (e *Engine) soloKaraokePlayAt(idx int) {
	track := e.soloKaraoke.queue[idx]
	e.soloKaraoke.index = idx
	e.soloKaraoke.currentTrack = track
	e.soloKaraoke.paused = false
	e.soloKaraoke.pausedAtMs = 0
	e.soloKaraoke.trackStartMs = nowMs()
	e.soloKaraoke.playToken = fmt.Sprintf("sk:%d:%s:%d", idx, track.ID, e.soloKaraoke.trackStartMs)

	if err := e.loadAndPlayTrack(track, 0); err != nil {
		// Surface it (s10eng-07) — without this, broadcastSoloKaraokeNowPlaying
		// just below still reports the track as current/playing even though
		// real playback never started, and no TrackEnded will ever arrive to
		// auto-advance away from it.
		e.broadcastSoloKaraokeError(fmt.Sprintf("playback failed to start: %v", err))
	}
	e.broadcastSoloKaraokeNowPlaying()
	// Clear any lingering lyrics from the previous track immediately so the
	// old lines don't bleed into the new song (mirrors startTrack's
	// identical clear-then-fetch sequencing for trivia).
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgLyrics, protocol.LyricsData{Lines: []protocol.LyricLine{}}))
	e.soloKaraokeFetchAndSendLyrics()
}

func (e *Engine) onAdminSoloKaraokePause(connID string, env protocol.ClientEnvelope) {
	if !e.soloKaraoke.active || e.soloKaraoke.currentTrack == nil || e.soloKaraoke.paused {
		return
	}
	e.soloKaraoke.pausedAtMs = nowMs() - e.soloKaraoke.trackStartMs
	e.soloKaraoke.paused = true
	_ = e.audio.Pause(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
}

func (e *Engine) onAdminSoloKaraokeResume(connID string, env protocol.ClientEnvelope) {
	if !e.soloKaraoke.active || e.soloKaraoke.currentTrack == nil || !e.soloKaraoke.paused {
		return
	}
	e.soloKaraoke.paused = false
	_ = e.audio.Resume(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{
		Action: "resume", PositionMs: e.soloKaraoke.pausedAtMs,
	}))
	e.soloKaraoke.trackStartMs = nowMs() - e.soloKaraoke.pausedAtMs
}

// onAdminSoloKaraokeStop ends the Solo Karaoke session entirely (as opposed
// to pause, which keeps it active): releases the mode-lock back to "none".
func (e *Engine) onAdminSoloKaraokeStop(connID string, env protocol.ClientEnvelope) {
	e.soloKaraokeDeactivate()
}

// soloKaraokeDeactivate ends the Solo Karaoke session (explicit stop OR
// queue exhaustion with no repeat): pauses stage playback, clears the
// current-track state, and releases the mode-lock. A no-op if already
// inactive (idempotent — queue exhaustion and a racing admin.stop can both
// call this without double-broadcasting).
func (e *Engine) soloKaraokeDeactivate() {
	if !e.soloKaraoke.active {
		return
	}
	e.soloKaraoke.active = false
	e.soloKaraoke.currentTrack = nil
	e.soloKaraoke.paused = false
	e.soloKaraoke.playToken = ""
	_ = e.audio.Pause(context.Background())
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
	e.broadcastSoloKaraokeActive()
	e.broadcastModeLock()
}

// onAdminSoloKaraokeNext manually skips forward. Shares soloKaraokeStepForward
// with the auto-advance path (soloKaraokeAdvance), EXCEPT a manual skip
// deliberately ignores repeat="one" (which only governs what happens when a
// track ends naturally) — an admin clicking "next" must always move
// forward, never replay the same song.
func (e *Engine) onAdminSoloKaraokeNext(connID string, env protocol.ClientEnvelope) {
	if !e.soloKaraoke.active {
		return
	}
	e.soloKaraokeStepForward()
}

// onAdminSoloKaraokePrev manually skips backward. Ignores shuffle (always
// steps linearly, mirroring most consumer players' prev-button behavior);
// clamps at the first track rather than wrapping, even under repeat="all" —
// there is no natural "previous" with no history under shuffle, and the
// simplest predictable behavior is preferred over inventing one.
func (e *Engine) onAdminSoloKaraokePrev(connID string, env protocol.ClientEnvelope) {
	if !e.soloKaraoke.active || len(e.soloKaraoke.queue) == 0 {
		return
	}
	idx := e.soloKaraoke.index - 1
	if idx < 0 {
		idx = 0
	}
	e.soloKaraokePlayAt(idx)
}

func (e *Engine) onAdminSoloKaraokeSeek(connID string, env protocol.ClientEnvelope) {
	if !e.soloKaraoke.active || e.soloKaraoke.currentTrack == nil {
		return
	}
	var d soloKaraokeSeekData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badSeek", err.Error())
		return
	}
	pos := d.PositionMs
	if pos < 0 {
		pos = 0
	}
	if e.soloKaraoke.currentTrack.DurationMs > 0 && pos > e.soloKaraoke.currentTrack.DurationMs {
		pos = e.soloKaraoke.currentTrack.DurationMs
	}
	wasPaused := e.soloKaraoke.paused
	if err := e.loadAndPlayTrack(e.soloKaraoke.currentTrack, pos); err != nil {
		e.broadcastSoloKaraokeError(fmt.Sprintf("seek failed: %v", err))
	}
	e.soloKaraoke.trackStartMs = nowMs() - pos
	if wasPaused {
		// loadAndPlayTrack just told the device to play — bring it back to
		// paused at the new position instead of leaving e.soloKaraoke.paused
		// true while audio is actually running (s10eng-01). Previously this
		// desynced Pause (dead until the next track, since it no-ops on
		// paused==true), Resume (would audibly jump back to the pre-seek
		// position), and the stage-reconnect resume logic (would tell a
		// reconnecting stage to pause a device that's actually playing).
		e.soloKaraoke.pausedAtMs = pos
		_ = e.audio.Pause(context.Background())
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgAudio, protocol.AudioData{Action: "pause"}))
	}
}

func (e *Engine) onAdminSoloKaraokeSetShuffle(connID string, env protocol.ClientEnvelope) {
	var d soloKaraokeSetShuffleData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badSetShuffle", err.Error())
		return
	}
	e.soloKaraoke.shuffle = d.Shuffle
	e.broadcastSoloKaraokeNowPlaying()
}

func (e *Engine) onAdminSoloKaraokeSetRepeat(connID string, env protocol.ClientEnvelope) {
	var d soloKaraokeSetRepeatData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		e.sendError(connID, "badSetRepeat", err.Error())
		return
	}
	if d.Repeat != "" && d.Repeat != "one" && d.Repeat != "all" {
		e.sendError(connID, "badSetRepeat", `repeat must be "", "one", or "all"`)
		return
	}
	e.soloKaraoke.repeat = d.Repeat
	e.broadcastSoloKaraokeNowPlaying()
}

// soloKaraokeAdvance handles the TrackEnded signal from the stage
// (onStagePlayerState) while Solo Karaoke is active: repeat="one" replays
// the current track exactly; anything else steps forward (soloKaraokeStepForward),
// which itself handles shuffle/repeat="all"/queue exhaustion.
func (e *Engine) soloKaraokeAdvance() {
	// s10eng-02 (duplicate/retransmitted TrackEnded signal double-advancing):
	// investigated and fixed at the actual source instead of here. There is
	// no per-track identifier in StagePlayerStateData, and by the time any
	// second call to this function runs, e.soloKaraoke's fields already
	// reflect whatever the FIRST call just advanced to — a token/flag
	// compared against the engine's OWN live state can't tell "a genuine new
	// end for the new track" apart from "a stale duplicate of the old one"
	// (proven by writing exactly that guard and watching it fail to catch a
	// back-to-back duplicate in a test). The real root cause is client-side:
	// web/stage/src/audio/spotify.ts's ingest() was reporting trackEnded as
	// a LEVEL (true on every sample while paused-at-position-0), not an EDGE
	// (true once, on the transition into that state) — the Spotify SDK can
	// fire player_state_changed more than once during a single track
	// transition, each one landing on the terminal state. Fixed there.
	if e.soloKaraoke.repeat == "one" {
		if e.soloKaraoke.index >= 0 && e.soloKaraoke.index < len(e.soloKaraoke.queue) {
			e.soloKaraokePlayAt(e.soloKaraoke.index)
		} else {
			// The currently-playing track was removed from the Playlist
			// bucket mid-song (ReloadSoloKaraokeQueue sets index = -1) while
			// repeat="one" was set — fall back to the queue-exhaustion-safe
			// step logic instead of ending the whole session outright
			// (s10eng-03): soloKaraokeStepForward already deactivates if and
			// only if the queue is genuinely empty.
			e.soloKaraokeStepForward()
		}
		return
	}
	e.soloKaraokeStepForward()
}

// soloKaraokeStepForward computes and plays the next track, OR ends the
// session if the queue is exhausted with nothing left to play. Shuffle
// (fresh random pick per advance, excluding the current track — the
// simplest correct behavior per the design doc, not a persisted
// permutation) never runs out: it always has a next pick as long as the
// queue isn't empty, so repeat is irrelevant under shuffle. Sequential
// playback honors repeat="all" (wrap to index 0) or ends the session with no
// repeat.
func (e *Engine) soloKaraokeStepForward() {
	n := len(e.soloKaraoke.queue)
	if n == 0 {
		e.soloKaraokeDeactivate()
		return
	}
	if e.soloKaraoke.shuffle {
		nextIdx := 0
		if n > 1 {
			for {
				nextIdx = e.rng.Intn(n)
				if nextIdx != e.soloKaraoke.index {
					break
				}
			}
		}
		e.soloKaraokePlayAt(nextIdx)
		return
	}
	nextIdx := e.soloKaraoke.index + 1
	if nextIdx >= n {
		if e.soloKaraoke.repeat == "all" {
			nextIdx = 0
		} else {
			e.soloKaraokeDeactivate()
			return
		}
	}
	e.soloKaraokePlayAt(nextIdx)
}

// soloKaraokeNowPlayingData builds the full-metadata now-playing payload
// (stage/admin only — see smsgSoloKaraokeNowPlaying's CONTRACT-QUESTION).
func (e *Engine) soloKaraokeNowPlayingData() soloKaraokeNowPlayingData {
	t := e.soloKaraoke.currentTrack
	if t == nil {
		return soloKaraokeNowPlayingData{
			Index: -1, QueueLen: len(e.soloKaraoke.queue),
			Shuffle: e.soloKaraoke.shuffle, Repeat: e.soloKaraoke.repeat,
		}
	}
	return soloKaraokeNowPlayingData{
		TrackID: t.ID, SpotifyURI: t.SpotifyURI, Artist: t.Artist, Song: t.Song,
		AlbumArt: t.AlbumArt, DurationMs: t.DurationMs,
		Index: e.soloKaraoke.index, QueueLen: len(e.soloKaraoke.queue),
		Shuffle: e.soloKaraoke.shuffle, Repeat: e.soloKaraoke.repeat,
		Paused: e.soloKaraoke.paused,
	}
}

// broadcastSoloKaraokeNowPlaying sends full track metadata to stage (to
// render the karaoke view) and admin (for the Player UI) ONLY — see
// smsgSoloKaraokeNowPlaying's CONTRACT-QUESTION; this must NEVER go out via
// BroadcastAll.
func (e *Engine) broadcastSoloKaraokeNowPlaying() {
	e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgSoloKaraokeNowPlaying, e.soloKaraokeNowPlayingData()))
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgSoloKaraokeNowPlaying, e.soloKaraokeNowPlayingData()))
}

// broadcastSoloKaraokeActive sends the one Solo Karaoke signal mobile may
// ever see — see smsgSoloKaraokeActive's CONTRACT-QUESTION.
func (e *Engine) broadcastSoloKaraokeActive() {
	e.bcast.BroadcastAll(e.envelope(smsgSoloKaraokeActive, soloKaraokeActiveData{Active: e.soloKaraoke.active}))
}

// broadcastSoloKaraokeError surfaces a playback failure to the admin Player
// UI (s10eng-07). Admin-only; no track metadata.
func (e *Engine) broadcastSoloKaraokeError(message string) {
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgSoloKaraokeError, soloKaraokeErrorData{Message: message}))
}

// modeLockOwner reports which mode currently owns the engine. Solo Karaoke
// being active implies trivia cannot also be mid-game (StartGame itself is
// gated on e.soloKaraoke.active), so the two conditions below are mutually
// exclusive by construction, not by the ordering of this switch.
func (e *Engine) modeLockOwner() string {
	switch {
	case e.soloKaraoke.active:
		return "solo_karaoke"
	case e.state != protocol.StateLobby && e.state != protocol.StateGameOver:
		return "trivia"
	default:
		return "none"
	}
}

// broadcastModeLock tells every open admin tab who currently owns the
// engine, called on every transition that can change the owner: StartGame,
// trivia -> GAME_OVER, and every Solo Karaoke active/inactive flip.
func (e *Engine) broadcastModeLock() {
	e.bcast.Broadcast(protocol.RoleAdmin, e.envelope(smsgModeLock, modeLockData{Owner: e.modeLockOwner()}))
}

// soloKaraokeFetchAndSendLyrics is a thin sibling of fetchAndSendLyrics
// (reveal.go/engine.go's trivia equivalent), reusing the same singleflight
// lyricFetch/prefetchLyrics cache unmodified (already parameterized by
// spotifyURI/artist/song/durSec, no round coupling) — staleness is gated on
// e.soloKaraoke.playToken instead of e.roundKey.
func (e *Engine) soloKaraokeFetchAndSendLyrics() {
	if e.lyrics == nil || e.soloKaraoke.currentTrack == nil {
		return
	}
	track := e.soloKaraoke.currentTrack
	spotifyURI, artist, song := track.SpotifyURI, track.Artist, track.Song
	durSec := int(track.DurationMs / 1000)
	tok := e.soloKaraoke.playToken
	ent := e.lyricFetch(spotifyURI, artist, song, durSec)

	send := func() {
		if e.soloKaraoke.playToken != tok {
			return // track moved on; drop stale lyrics
		}
		if ent.err != nil || len(ent.lines) == 0 {
			e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgSoloKaraokeLyricsStatus, soloKaraokeLyricsStatusData{Status: "none"}))
			return
		}
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgSoloKaraokeLyricsStatus, soloKaraokeLyricsStatusData{Status: "ready"}))
		e.bcast.Broadcast(protocol.RoleStage, e.envelope(protocol.SMsgLyrics, protocol.LyricsData{Lines: ent.lines}))
	}

	select {
	case <-ent.done:
		// Already fetched (prior prefetch/fetch for this song): serve
		// immediately, no spinner.
		send()
	default:
		// Still in flight: wait for the shared fetch, then serve. Only show
		// a spinner if it doesn't land within a short grace window.
		go func() {
			select {
			case <-ent.done:
			case <-time.After(400 * time.Millisecond):
				e.submit(func() {
					if e.soloKaraoke.playToken == tok {
						e.bcast.Broadcast(protocol.RoleStage, e.envelope(smsgSoloKaraokeLyricsStatus, soloKaraokeLyricsStatusData{Status: "loading"}))
					}
				})
				<-ent.done
			}
			e.submit(send)
		}()
	}
}

package game

import (
	"hash/fnv"
	"math/rand"
	"strings"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// reveal.go — server-authoritative letter-by-letter decrypt reveal.
//
// The reveal that used to be animated client-side on the stage (which held the
// full answer) is now driven by the server, which owns the reveal clock and
// broadcasts a MASKED frame to BOTH stage and mobile in the SAME broadcast.
// Mobile can therefore never learn a letter before the projector shows it — the
// only answer-derived data a phone receives is an already-revealed letter, and
// only because the identical frame went to the stage in the same tick. This is
// the §4A boundary extended: mobile still never receives the trusted SMsgReveal
// (full text / uri / albumArt / lyrics), only the mask.
//
// CONTRACT-QUESTION: smsgMaskedReveal is a new server message type. It is
// defined here (not in the fixed-contract protocol.go) per project rules; if
// accepted it should move into protocol.go on a version bump. Mirrored in
// web/shared/protocol.ts.
const smsgMaskedReveal protocol.ServerMsgType = "maskedReveal"

// CONTRACT-QUESTION: smsgAdminRevealCfg echoes the current reveal-timing knob
// values to the admin control room so its sliders reflect server truth (the
// legacy skip-threshold knob is write-only; this one is not). Admin-only.
const smsgAdminRevealCfg protocol.ServerMsgType = "adminRevealCfg"

// CONTRACT-QUESTION: cmsgAdminSetRevealCfg lets the control room tune the
// reveal-timing knobs live. Applies to the NEXT round (the in-flight reveal
// clock snapshots its settings at startTrack).
const cmsgAdminSetRevealCfg protocol.ClientMsgType = "admin.setRevealCfg"

// CONTRACT-QUESTION: smsgLyricsStatus tells the stage whether synced lyrics are
// still being fetched ("loading") or definitively absent ("none"), so it can
// show a spinner instead of flashing "no lyrics" during the ~seconds-long
// LRCLIB fetch. The actual lines still arrive via the fixed SMsgLyrics frame.
const smsgLyricsStatus protocol.ServerMsgType = "lyricsStatus"

// Reveal timing defaults (ms). Mirror the old client-side constants in
// web/stage/src/anim/decrypt.ts (PHASE1_MS, REVEAL_INTERVAL_MS). Overridable
// per-deploy via env (see cmd/gameserver) and live via the admin knob.
const (
	defaultRevealIntervalMs = 3000  // ms between revealed letters
	defaultRevealPhase1Ms   = 10000 // letters start streaming after this
	defaultRevealBlockMs    = 15000 // hide the real length behind a block until this
	defaultRevealEaseMs     = 3000  // soft morph when the block collapses to real length
	revealBlockWidth        = 16    // fixed-width noise block shown during the block phase
)

// Clamp bounds for the live knob so a fat-fingered slider can't wedge the game.
const (
	minRevealIntervalMs = 250
	maxRevealIntervalMs = 10000
	minRevealPhase1Ms   = 0
	maxRevealPhase1Ms   = 20000
	minRevealBlockMs    = 0
	maxRevealBlockMs    = 20000
	minRevealEaseMs     = 0
	maxRevealEaseMs     = 5000
)

// revealConfig holds the tunable reveal-timing knobs. Stored on the engine and
// mutated by the admin handler; snapshotted into revealClock at startTrack so a
// mid-round change applies to the next round, not the running ticker.
type revealConfig struct {
	IntervalMs int  // ms between revealed letters
	Phase1Ms   int  // time before letters start streaming
	BlockMs    int  // hide the real length behind a fixed-width block until this time (0 = off)
	EaseMs     int  // soft morph duration when the block collapses to the real length
	Alternate  bool // true: one field per tick (artist,song,artist,...); false: one from each per tick
}

// defaultRevealConfig returns the built-in defaults.
func defaultRevealConfig() revealConfig {
	return revealConfig{
		IntervalMs: defaultRevealIntervalMs,
		Phase1Ms:   defaultRevealPhase1Ms,
		BlockMs:    defaultRevealBlockMs,
		EaseMs:     defaultRevealEaseMs,
		Alternate:  true,
	}
}

// clamp keeps the timing knobs within sane bounds.
func (c revealConfig) clamp() revealConfig {
	clampInt := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	c.IntervalMs = clampInt(c.IntervalMs, minRevealIntervalMs, maxRevealIntervalMs)
	c.Phase1Ms = clampInt(c.Phase1Ms, minRevealPhase1Ms, maxRevealPhase1Ms)
	c.BlockMs = clampInt(c.BlockMs, minRevealBlockMs, maxRevealBlockMs)
	c.EaseMs = clampInt(c.EaseMs, minRevealEaseMs, maxRevealEaseMs)
	return c
}

// letterStartDelay is when letter streaming begins: after the phase-1 delay, but
// never before the block has collapsed to the real length.
func (c revealConfig) letterStartMs() int {
	if c.BlockMs > c.Phase1Ms {
		return c.BlockMs
	}
	return c.Phase1Ms
}

// Reveal phases, mirroring the stage animation's phase semantics.
const (
	revealPhaseNoise    = 1 // fixed-width noise block, no answer length shown
	revealPhaseSkeleton = 2 // answer length + spaces visible, no letters yet
	revealPhaseStream   = 3 // letters trickling in
	revealPhaseDone     = 4 // fully revealed
)

// revealClock drives the letter-by-letter reveal for the current round. All
// fields are owned by the Run goroutine (mutated only inside submitted fns).
// It is count-based, not wall-clock-based: pausing on a buzz is simply "don't
// advance", which sidesteps the elapsed-time re-anchoring the point timer needs.
type revealClock struct {
	active   bool   // a reveal is armed for the current round
	roundKey string // e.roundKey this clock belongs to (staleness guard)

	artist string // uppercased answer text — SERVER-SIDE ONLY, never in a frame
	song   string
	artistOrder []int // random reveal order of non-space indices in artist
	songOrder   []int // random reveal order of non-space indices in song

	artistRevealed int // count of artist letters revealed so far
	songRevealed   int // count of song letters revealed so far

	phase      int  // revealPhase*
	phase1Done bool // the noise one-shot has fired
	finalizing bool // a final (KARAOKE/daily-double) full reveal is in flight
	nextField  int  // 0 = artist's turn, 1 = song's turn (alternation cursor)

	cfg revealConfig // snapshot of the knobs for THIS round
}

// buildRevealOrder returns the non-space character indices of s in a
// deterministic shuffled order, seeded so a given round is reproducible (Go
// port of buildRevealOrder in web/stage/src/anim/decrypt.ts).
func buildRevealOrder(s string, seed uint64) []int {
	idx := make([]int, 0, len(s))
	for i, r := range s {
		// Byte-index space check is fine: titles are treated per-byte for masking.
		if r != ' ' {
			idx = append(idx, i)
		}
	}
	rng := rand.New(rand.NewSource(int64(seed)))
	rng.Shuffle(len(idx), func(a, b int) { idx[a], idx[b] = idx[b], idx[a] })
	return idx
}

// seedFromRoundKey hashes the per-round key to a stable seed so the reveal order
// (and thus tests) is deterministic per round.
func seedFromRoundKey(roundKey string, salt uint64) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(roundKey))
	return h.Sum64() ^ salt
}

// buildFieldMask returns a per-character mask for one field. Each element is:
//   - the revealed character, for a slot whose index is among the first
//     `revealed` entries of `order`;
//   - " " for a space (always visible — word boundaries are not secret);
//   - "" for a not-yet-revealed letter slot (client renders local noise).
//
// The array length equals len(field), so spaces/underscores in a title are
// unambiguous (unlike a single string with placeholder chars).
func buildFieldMask(field string, order []int, revealed int) []string {
	mask := make([]string, len(field))
	for i := 0; i < len(field); i++ {
		if field[i] == ' ' {
			mask[i] = " "
		} else {
			mask[i] = ""
		}
	}
	if revealed > len(order) {
		revealed = len(order)
	}
	for i := 0; i < revealed; i++ {
		idx := order[i]
		mask[idx] = string(field[idx])
	}
	return mask
}

// maskedRevealData is the sanitized decrypt frame (CONTRACT-QUESTION payload for
// smsgMaskedReveal). It carries ONLY already-revealed letters plus phase and
// lengths — never the raw answer text, album art, or reveal order.
type maskedRevealData struct {
	Phase     int      `json:"phase"`
	ArtistLen int      `json:"artistLen"`
	SongLen   int      `json:"songLen"`
	Artist    []string `json:"artist"` // per-char mask; "" hidden, " " space, else revealed char
	Song      []string `json:"song"`
	Final     bool     `json:"final"`
	// EaseMs tells the client how long to morph the fixed-width block into the
	// real-length skeleton when phase transitions Noise->Skeleton (cosmetic; the
	// block carries no answer info so animating it client-side is §4A-safe).
	EaseMs int `json:"easeMs,omitempty"`
}

// adminRevealCfgData echoes the current knob values to the admin UI.
type adminRevealCfgData struct {
	IntervalMs int  `json:"intervalMs"`
	Phase1Ms   int  `json:"phase1Ms"`
	BlockMs    int  `json:"blockMs"`
	EaseMs     int  `json:"easeMs"`
	Alternate  bool `json:"alternate"`
}

// adminSetRevealCfgData is the admin -> server knob update. Pointer fields let
// the client send a partial update (only the sliders it touched).
type adminSetRevealCfgData struct {
	IntervalMs *int  `json:"intervalMs"`
	Phase1Ms   *int  `json:"phase1Ms"`
	BlockMs    *int  `json:"blockMs"`
	EaseMs     *int  `json:"easeMs"`
	Alternate  *bool `json:"alternate"`
}

// currentMask builds the frame for the current reveal state. During the block
// phase it emits a FIXED-WIDTH all-hidden mask (revealBlockWidth slots, no real
// length), so the true answer length stays secret until the block collapses.
func (rc *revealClock) currentMask() maskedRevealData {
	phase := rc.phase
	final := rc.finalizing || phase == revealPhaseDone
	if phase == revealPhaseNoise {
		block := make([]string, revealBlockWidth)
		for i := range block {
			block[i] = "" // all hidden noise; no spaces, no length hint
		}
		return maskedRevealData{
			Phase:     revealPhaseNoise,
			ArtistLen: revealBlockWidth,
			SongLen:   revealBlockWidth,
			Artist:    block,
			Song:      append([]string(nil), block...),
			EaseMs:    rc.cfg.EaseMs,
		}
	}
	return maskedRevealData{
		Phase:     phase,
		ArtistLen: len(rc.artist),
		SongLen:   len(rc.song),
		Artist:    buildFieldMask(rc.artist, rc.artistOrder, rc.artistRevealed),
		Song:      buildFieldMask(rc.song, rc.songOrder, rc.songRevealed),
		Final:     final,
		EaseMs:    rc.cfg.EaseMs,
	}
}

// artistDone/songDone report whether a field has all its letters revealed.
func (rc *revealClock) artistDone() bool { return rc.artistRevealed >= len(rc.artistOrder) }
func (rc *revealClock) songDone() bool   { return rc.songRevealed >= len(rc.songOrder) }
func (rc *revealClock) allDone() bool    { return rc.artistDone() && rc.songDone() }

// revealOneLetter advances the reveal by a single letter, honoring the
// alternation setting. Returns false if there was nothing left to reveal.
func (rc *revealClock) revealOneLetter() bool {
	if rc.allDone() {
		return false
	}
	if !rc.cfg.Alternate {
		// One from each field per tick.
		advanced := false
		if !rc.artistDone() {
			rc.artistRevealed++
			advanced = true
		}
		if !rc.songDone() {
			rc.songRevealed++
			advanced = true
		}
		return advanced
	}
	// Alternate artist -> song -> artist, skipping a finished field.
	for tries := 0; tries < 2; tries++ {
		if rc.nextField == 0 {
			rc.nextField = 1
			if !rc.artistDone() {
				rc.artistRevealed++
				return true
			}
		} else {
			rc.nextField = 0
			if !rc.songDone() {
				rc.songRevealed++
				return true
			}
		}
	}
	return false
}

// completeAll fills every remaining letter (used for the final KARAOKE /
// daily-double reveal).
func (rc *revealClock) completeAll() {
	rc.artistRevealed = len(rc.artistOrder)
	rc.songRevealed = len(rc.songOrder)
	rc.phase = revealPhaseDone
}

// upper normalizes answer text for display (the stage historically renders the
// decrypt in uppercase glyphs).
func upper(s string) string { return strings.ToUpper(s) }

// revealInterval / phase1Delay expose the snapshot durations as time.Duration.
func (rc *revealClock) revealInterval() time.Duration {
	return time.Duration(rc.cfg.IntervalMs) * time.Millisecond
}
func (rc *revealClock) phase1Delay() time.Duration {
	return time.Duration(rc.cfg.Phase1Ms) * time.Millisecond
}

package protocol

// GameState is the authoritative game lifecycle flag. This is the ONLY state
// information the mobile client is trusted with (design_doc §4A). The full
// game model lives server-side; clients render purely off this flag plus the
// sanitized payloads.
type GameState string

const (
	StateLobby       GameState = "LOBBY"        // QR code + joining (§3.1)
	StateBoard       GameState = "BOARD"        // grid shown, awaiting cell selection (§8A board view)
	StateRoundActive GameState = "ROUND_ACTIVE" // track playing, buzzer armed (§3.3-3.4)
	StateLocked      GameState = "LOCKED_OUT"   // a player buzzed; audio paused, awaiting grade (§3.4)
	StateAdjudicate  GameState = "ADJUDICATE"   // admin grading the oral answer (§3.6)
	StateKaraoke     GameState = "KARAOKE"      // track solved, lyrics + skip voting (§3.7-3.8)
	StateDailyDouble GameState = "DAILY_DOUBLE" // performer on stage, crowd 5-star rating (§7)
	StateTransition  GameState = "TRANSITION"   // "Next track in 3..." countdown (§3.9)
	StateGameOver    GameState = "GAME_OVER"
)

// AudienceFor maps a role to whether it may receive trusted reveal payloads.
// The transport layer / engine consults this before serializing reveal,
// lyrics, or admin-view frames. Mobile is never trusted.
func TrustedReveal(r Role) bool {
	return r == RoleStage || r == RoleAdmin
}

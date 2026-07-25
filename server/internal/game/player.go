package game

import (
	"unicode"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// player.go manages the live attendee registry and the Active-User pool math
// for skip voting (design_doc §3.2 ephemeral resume, §3.8 active pool).
//
// All access goes through the engine's single command loop, so these helpers
// are not independently locked — the engine owns serialization.

// conn is a single live connection. Multiple conns can map to one Player
// (e.g. an admin tab + the stage tab), and a mobile Player keeps its identity
// across reconnects via its device fingerprint (§3.2).
type conn struct {
	id       string
	role     protocol.Role
	playerID string // "" until Hello is processed
}

// registry holds the live connection + player tables. A Player is keyed by
// device fingerprint so a dropped attendee resumes their exact score (§3.2).
type registry struct {
	conns    map[string]*conn           // connID -> conn
	players  map[string]*Player         // playerID -> Player
	byFP     map[string]string          // deviceFP -> playerID
	connByPl map[string]map[string]bool // playerID -> set of connIDs
}

func newRegistry() *registry {
	return &registry{
		conns:    map[string]*conn{},
		players:  map[string]*Player{},
		byFP:     map[string]string{},
		connByPl: map[string]map[string]bool{},
	}
}

// addConn registers a bare connection before any Hello (ports.OnConnect).
func (r *registry) addConn(connID string) {
	if _, ok := r.conns[connID]; !ok {
		r.conns[connID] = &conn{id: connID}
	}
}

// removeConn drops a connection and reports the player it belonged to (if any)
// and whether that player now has zero live connections (fully disconnected).
func (r *registry) removeConn(connID string) (playerID string, fullyGone bool) {
	c := r.conns[connID]
	if c == nil {
		return "", false
	}
	delete(r.conns, connID)
	if c.playerID == "" {
		return "", false
	}
	set := r.connByPl[c.playerID]
	delete(set, connID)
	if len(set) == 0 {
		delete(r.connByPl, c.playerID)
		return c.playerID, true
	}
	return c.playerID, false
}

// resolvePlayer binds a Hello to a Player, resuming by fingerprint when known
// (§3.2). Returns the Player and whether it was newly created.
func (r *registry) resolvePlayer(connID string, h protocol.HelloData) (*Player, bool) {
	c := r.conns[connID]
	if c == nil {
		c = &conn{id: connID}
		r.conns[connID] = c
	}
	c.role = h.Role

	var p *Player
	created := false
	if id, ok := r.byFP[h.DeviceFP]; ok && h.DeviceFP != "" {
		p = r.players[id]
	}
	if p == nil {
		id := h.DeviceFP
		if id == "" {
			id = connID // non-fingerprinted roles (stage/admin) key on conn
		}
		p = &Player{ID: id, DeviceFP: h.DeviceFP, Active: true}
		r.players[id] = p
		if h.DeviceFP != "" {
			r.byFP[h.DeviceFP] = id
		}
		created = true
	}
	if h.Handle != "" {
		if sanitized := sanitizeHandle(h.Handle); sanitized != "" {
			p.Handle = sanitized
		}
	}
	c.playerID = p.ID
	if r.connByPl[p.ID] == nil {
		r.connByPl[p.ID] = map[string]bool{}
	}
	r.connByPl[p.ID][connID] = true
	return p, created
}

// sanitizeHandle strips control characters and clamps a player-supplied handle
// to 24 runes. The handle is broadcast (scoreboard/lockout/telemetry) to every
// role, so an unbounded or control-char-laden handle is a mobile-side injection
// vector. Returns "" if nothing usable remains (caller keeps the prior handle).
func sanitizeHandle(s string) string {
	out := make([]rune, 0, 24)
	for _, r := range s {
		// Drop control (Cc), format/bidi/zero-width (Cf: U+202E, U+200B/E/F,
		// U+2066-2069, U+FEFF), surrogates (Cs) and private-use (Co) runes —
		// all are render/injection vectors that survive an IsControl-only strip.
		// EXCEPT U+200D ZERO WIDTH JOINER (Cf): it is required to bind legitimate
		// emoji ZWJ sequences (👨‍👩‍👧, 👨‍💻); stripping it shatters a valid emoji
		// handle. It carries no bidi/reorder risk, so keep it (QA sweep 3 s3-sanitize).
		if r != '\u200d' &&
			(unicode.IsControl(r) ||
				unicode.Is(unicode.Cf, r) ||
				unicode.Is(unicode.Cs, r) ||
				unicode.Is(unicode.Co, r)) {
			continue
		}
		// A leading combining mark (U+0300-036F etc.) renders on the prior glyph
		// in stage/admin, so skip marks until we have a base rune.
		if len(out) == 0 && unicode.Is(unicode.M, r) {
			continue
		}
		out = append(out, r)
		if len(out) == 24 {
			break
		}
	}
	return string(out)
}

// connection returns the conn for a connID.
func (r *registry) connection(connID string) *conn { return r.conns[connID] }

// playerForConn returns the Player a connection is bound to, or nil.
func (r *registry) playerForConn(connID string) *Player {
	c := r.conns[connID]
	if c == nil || c.playerID == "" {
		return nil
	}
	return r.players[c.playerID]
}

// connIDs returns all live connection IDs for a player.
func (r *registry) connIDs(playerID string) []string {
	out := make([]string, 0, len(r.connByPl[playerID]))
	for id := range r.connByPl[playerID] {
		out = append(out, id)
	}
	return out
}

// online reports whether a player currently has at least one live connection.
func (r *registry) online(playerID string) bool {
	return len(r.connByPl[playerID]) > 0
}

// mobilePlayers returns all mobile-role players (one per identity).
func (r *registry) mobilePlayers() []*Player {
	out := []*Player{}
	for _, p := range r.players {
		// Stage/admin identities key on connID; mobile ones carry a fingerprint
		// or at least guess/vote. We treat any player bound to a mobile conn as
		// mobile by checking its connections' roles.
		if r.isMobile(p.ID) {
			out = append(out, p)
		}
	}
	return out
}

func (r *registry) isMobile(playerID string) bool {
	for connID := range r.connByPl[playerID] {
		if c := r.conns[connID]; c != nil && c.role == protocol.RoleMobile {
			return true
		}
	}
	return false
}

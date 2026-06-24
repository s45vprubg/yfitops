package anticheat

import "testing"

func TestEffectiveBuzzTime_CapsDeduction(t *testing.T) {
	// Normal RTT 40ms -> half 20ms deduction.
	if got := EffectiveBuzzTime(1000, 40); got != 980 {
		t.Errorf("rtt=40 -> %d, want 980", got)
	}
	// Forged huge RTT (2000ms): half=1000 but capped at 50ms (§4C).
	if got := EffectiveBuzzTime(1000, 2000); got != 950 {
		t.Errorf("forged rtt=2000 -> %d, want 950 (capped)", got)
	}
	// Exactly at cap boundary: rtt=100 -> half=50.
	if got := EffectiveBuzzTime(1000, 100); got != 950 {
		t.Errorf("rtt=100 -> %d, want 950", got)
	}
	// Negative/garbage RTT clamps to 0 deduction.
	if got := EffectiveBuzzTime(1000, -5); got != 1000 {
		t.Errorf("rtt=-5 -> %d, want 1000", got)
	}
}

func TestNonceGate_RejectsStale(t *testing.T) {
	g := NewNonceGate([]byte("k"))
	n0 := g.Current()
	if !g.Validate(n0) {
		t.Fatal("current nonce should validate")
	}
	g.Bump()
	if g.Validate(n0) {
		t.Error("stale nonce after Bump should be rejected (§4D)")
	}
	if !g.Validate(g.Current()) {
		t.Error("new current nonce should validate")
	}
}

func TestNonceGate_TokenChangesOnBump(t *testing.T) {
	g := NewNonceGate([]byte("secret"))
	t0 := g.Token()
	g.Bump()
	t1 := g.Token()
	if t0 == t1 {
		t.Error("token must change after Bump so clients cannot replay")
	}
	if len(t0) != 64 {
		t.Errorf("token len = %d, want 64 hex chars", len(t0))
	}
}

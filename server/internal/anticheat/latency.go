// Package anticheat implements the adversarial controls from design_doc §4.
// These are FIXED CONTRACTS: the latency-compensation formula and nonce
// semantics are referenced by the game engine and verified by tests.
package anticheat

// LatencyCapMs is the maximum full-RTT balance considered (design_doc §4C):
// the deduction is capped so a malicious actor forging an artificially high
// ping cannot earn an unbounded reaction-time advantage.
const LatencyCapMs = 100

// MaxHalfDeductMs is the cap on the one-way (RTT/2) execution deduction = 50ms.
const MaxHalfDeductMs = 50

// EffectiveBuzzTime computes the latency-compensated buzz time
// (design_doc §4C):
//
//	Effective_Time = Arrival_Time - min(RTT/2, 50ms)
//
// arrivalMs is the SERVER arrival clock (§4B) — client timestamps are never
// trusted. rttMs is the moving-average RTT from heartbeats. The deduction is
// capped at 50ms so forged high pings cannot buy advantage.
func EffectiveBuzzTime(arrivalMs int64, rttMs int) int64 {
	half := rttMs / 2
	if half > MaxHalfDeductMs {
		half = MaxHalfDeductMs
	}
	if half < 0 {
		half = 0
	}
	return arrivalMs - int64(half)
}

package game

import "math"

// Scoring implements design_doc §7 "Difficulty Scaling & Scoring Matrix".
//
// Model: Base + Bonus with linear time decay. ALL rounding uses math.Floor
// per spec, so the backend is the single source of truth and the stage's
// 60fps visual timer (which applies the same formula) matches to the floor.
//
// This file is a FIXED CONTRACT — the stage frontend reimplements the same
// decay formula in JS and must stay bit-identical at the floor. Keep the
// constants and the formula here authoritative; web/shared mirrors them.

const (
	// BaseValue is awarded to any fully-correct guess regardless of timing (§7).
	BaseValue = 100

	// DecayHoldSeconds: the track holds max value for the first 5s (§7).
	DecayHoldSeconds = 5.0
	// DecayEndSeconds: bonus reaches 0 at the 60s mark (§7).
	DecayEndSeconds = 60.0

	// PartialPoints: a partial guess (artist OR song only) is worth exactly
	// half the base = 50 points (§7). The remaining 50 base + remaining
	// time-decayed bonus stay alive for the next guesser.
	PartialPoints = 50
)

// RowMultiplier returns the difficulty multiplier for a 1-indexed grid row
// (§7): Row1 +0%, Row2 +25%, Row3 +50%, Row4 +75%, Row5 +100%.
// Returns the multiplier as a float (1.0 .. 2.0). Rows outside 1..5 clamp.
func RowMultiplier(row int) float64 {
	switch {
	case row <= 1:
		return 1.00
	case row == 2:
		return 1.25
	case row == 3:
		return 1.50
	case row == 4:
		return 1.75
	default: // row >= 5
		return 2.00
	}
}

// MaxPointsForRow is the ceiling for a track in the given row:
// Row1=100, Row2=125, Row3=150, Row4=175, Row5=200 (§7).
func MaxPointsForRow(row int) int {
	return int(math.Floor(BaseValue * RowMultiplier(row)))
}

// BonusCeiling is the decaying portion = max - base. For row 1 this is 0
// (no bonus to decay); for row 5 it's 100.
func BonusCeiling(row int) int {
	return MaxPointsForRow(row) - BaseValue
}

// CurrentPoints returns the floored point value available at elapsedMs into
// the track for the given row, per the linear decay curve (§7):
//   - t <= 5s:        max value
//   - 5s < t < 60s:   base + bonus * (1 - (t-5)/55)
//   - t >= 60s:       base only
func CurrentPoints(row int, elapsedMs int64) int {
	t := float64(elapsedMs) / 1000.0
	bonus := float64(BonusCeiling(row))

	switch {
	case t <= DecayHoldSeconds:
		return MaxPointsForRow(row)
	case t >= DecayEndSeconds:
		return BaseValue
	default:
		span := DecayEndSeconds - DecayHoldSeconds // 55s
		frac := 1.0 - (t-DecayHoldSeconds)/span    // 1.0 -> 0.0
		return int(math.Floor(BaseValue + bonus*frac))
	}
}

// PartialAward computes the points for a partial guess and the remaining pool
// that stays alive for a subsequent guesser (§7).
//
// Returns:
//   - awarded:   points given to the partial guesser (exactly 50)
//   - remaining: the leftover pool = remaining 50 base + current decayed bonus.
//
// The remaining bonus keeps decaying after audio resumes; remaining here is the
// snapshot at the moment of the partial guess. The next full-half guesser
// claims base/2 (50) + whatever bonus remains at THEIR guess time.
func PartialAward(row int, elapsedMs int64) (awarded, remaining int) {
	current := CurrentPoints(row, elapsedMs)
	// current = 100 base + decayedBonus. Partial takes 50, leaving 50 + bonus.
	awarded = PartialPoints
	remaining = current - PartialPoints
	if remaining < 0 {
		remaining = 0
	}
	return awarded, remaining
}

// DailyDoubleMultiplier maps an average crowd star rating to the performance
// bonus multiplier applied to the max track value (§7):
// 5★ = 2.0x (double), 4★ = +75%, 3★ = +50%, 2★ = +25%, 1★ = +0%.
func DailyDoubleMultiplier(avgStars float64) float64 {
	switch {
	case avgStars >= 4.5:
		return 2.00
	case avgStars >= 3.5:
		return 1.75
	case avgStars >= 2.5:
		return 1.50
	case avgStars >= 1.5:
		return 1.25
	default:
		return 1.00
	}
}
